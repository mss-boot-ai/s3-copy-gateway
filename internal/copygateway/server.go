package copygateway

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/aws/smithy-go"
)

const copySourceHeader = "x-amz-copy-source"

var unsupportedCopyHeaders = map[string]struct{}{
	"x-amz-copy-source-if-match":                                  {},
	"x-amz-copy-source-if-none-match":                             {},
	"x-amz-copy-source-if-modified-since":                         {},
	"x-amz-copy-source-if-unmodified-since":                       {},
	"x-amz-copy-source-range":                                     {},
	"x-amz-acl":                                                   {},
	"x-amz-grant-read":                                            {},
	"x-amz-grant-write":                                           {},
	"x-amz-grant-read-acp":                                        {},
	"x-amz-grant-write-acp":                                       {},
	"x-amz-grant-full-control":                                    {},
	"x-amz-storage-class":                                         {},
	"x-amz-server-side-encryption":                                {},
	"x-amz-server-side-encryption-aws-kms-key-id":                 {},
	"x-amz-server-side-encryption-context":                        {},
	"x-amz-server-side-encryption-bucket-key-enabled":             {},
	"x-amz-server-side-encryption-customer-algorithm":             {},
	"x-amz-server-side-encryption-customer-key":                   {},
	"x-amz-server-side-encryption-customer-key-md5":               {},
	"x-amz-copy-source-server-side-encryption-customer-algorithm": {},
	"x-amz-copy-source-server-side-encryption-customer-key":       {},
	"x-amz-copy-source-server-side-encryption-customer-key-md5":   {},
	"x-amz-website-redirect-location":                             {},
	"x-amz-object-lock-mode":                                      {},
	"x-amz-object-lock-retain-until-date":                         {},
	"x-amz-object-lock-legal-hold":                                {},
	"x-amz-checksum-algorithm":                                    {},
	"x-amz-tagging":                                               {},
	"x-amz-request-payer":                                         {},
	"x-amz-expected-bucket-owner":                                 {},
	"x-amz-source-expected-bucket-owner":                          {},
}

type Server struct {
	cfg    Config
	auth   *Authenticator
	source SourceChecker
	target CopyTarget
	sem    chan struct{}
	logger *slog.Logger

	sourceErrorLogAt atomic.Int64
	targetErrorLogAt atomic.Int64
}

type copyObjectResultXML struct {
	XMLName      xml.Name `xml:"CopyObjectResult"`
	LastModified string   `xml:"LastModified"`
	ETag         string   `xml:"ETag,omitempty"`
}

func NewServer(cfg Config, source SourceChecker, target CopyTarget, logger *slog.Logger) (*Server, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if source == nil {
		return nil, errors.New("source checker is required")
	}
	if target == nil {
		return nil, errors.New("copy target is required")
	}
	auth, err := NewAuthenticator(cfg.Auth)
	if err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		cfg:    cfg,
		auth:   auth,
		source: source,
		target: target,
		sem:    make(chan struct{}, cfg.MaxInFlight),
		logger: logger,
	}, nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
		return
	}
	if s3err := validateCopyPreflight(r); s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	targetAddress, s3err := parseAddress(r, s.cfg)
	if s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	if _, s3err = s.auth.Verify(r); s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	if s3err = validatePayloadHash(r); s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	sourceAddress, s3err := parseCopySourceHeader(r.Header.Get(copySourceHeader))
	if s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	cloudSource, s3err := s.cfg.Buckets.Resolve(sourceAddress.Bucket, sourceAddress.Key, s.cfg.Target.Region)
	if s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	cloudTarget, s3err := s.cfg.Buckets.Resolve(targetAddress.Bucket, targetAddress.Key, s.cfg.Target.Region)
	if s3err != nil {
		writeS3Error(w, s3err)
		return
	}
	if cloudSource.Region != cloudTarget.Region {
		writeS3Error(w, errBadRequest("cross-region CopyObject is not supported"))
		return
	}
	if !s.acquire(r.Context()) {
		w.Header().Set("Retry-After", "5")
		writeS3Error(w, errSlowDown())
		return
	}
	defer s.release()

	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.CopyTimeout)
	defer cancel()
	result, s3err := s.executeCopy(ctx,
		ObjectRef{Bucket: sourceAddress.Bucket, Key: sourceAddress.Key, Region: s.cfg.Source.Region},
		cloudSource,
		cloudTarget,
	)
	if s3err != nil {
		if s3err.Code == "SlowDown" {
			w.Header().Set("Retry-After", "5")
		}
		writeS3Error(w, s3err)
		return
	}
	writeCopyObjectResult(w, result)
}

func validateCopyPreflight(r *http.Request) *S3Error {
	if r.Method != http.MethodPut {
		return errMethodNotAllowed()
	}
	if strings.TrimSpace(r.Header.Get(copySourceHeader)) == "" {
		return errNotImplemented("only CopyObject is implemented")
	}
	if r.ContentLength < 0 {
		return errBadRequest("CopyObject requests must have a known empty body")
	}
	if r.ContentLength > 0 {
		return errBadRequest("CopyObject requests must not include a request body")
	}
	query := r.URL.Query()
	for _, subresource := range []string{"uploadId", "partNumber", "uploads", "acl", "tagging", "legal-hold", "retention", "torrent"} {
		if _, ok := query[subresource]; ok {
			return errNotImplemented("only CopyObject is implemented")
		}
	}
	if directive := strings.TrimSpace(r.Header.Get("x-amz-metadata-directive")); directive != "" && !strings.EqualFold(directive, "COPY") {
		return errNotImplemented("CopyObject metadata replacement is not implemented")
	}
	if directive := strings.TrimSpace(r.Header.Get("x-amz-tagging-directive")); directive != "" && !strings.EqualFold(directive, "COPY") {
		return errNotImplemented("CopyObject tagging replacement is not implemented")
	}
	for header, values := range r.Header {
		lower := strings.ToLower(header)
		if _, unsupported := unsupportedCopyHeaders[lower]; !unsupported {
			continue
		}
		for _, value := range values {
			if strings.TrimSpace(value) != "" {
				return errNotImplemented("unsupported CopyObject option: " + lower)
			}
		}
	}
	return nil
}

func validatePayloadHash(r *http.Request) *S3Error {
	if strings.EqualFold(strings.TrimSpace(r.Header.Get(payloadHashHeader)), payloadStreaming) {
		return errNotImplemented("aws-chunked streaming payloads are not supported")
	}
	value := strings.TrimSpace(r.Header.Get(payloadHashHeader))
	if value != payloadUnsigned && !isHexSHA256(value) {
		return errBadRequest("unsupported x-amz-content-sha256 payload mode")
	}
	return nil
}

func (s *Server) executeCopy(ctx context.Context, localSource, cloudSource, cloudTarget ObjectRef) (*ObjectResult, *S3Error) {
	checkCtx, cancel := context.WithTimeout(ctx, s.cfg.SourceCheckTimeout)
	result, err := s.source.HeadObject(checkCtx, localSource)
	if err == nil {
		cancel()
		return result, nil
	}
	if !isObjectNotFound(err) {
		s.logUpstreamError(&s.sourceErrorLogAt, "source S3 HeadObject failed", "bucket", localSource.Bucket, "error", err)
		s3err := classifyUpstreamError(checkCtx, err, "source S3 availability check failed")
		cancel()
		return nil, s3err
	}
	cancel()

	result, err = s.target.CopyObject(ctx, CopyObjectRequest{Source: cloudSource, Target: cloudTarget})
	if err != nil {
		s.logUpstreamError(&s.targetErrorLogAt, "target S3 CopyObject failed", "source_bucket", cloudSource.Bucket, "target_bucket", cloudTarget.Bucket, "error", err)
		return nil, classifyTargetCopyError(ctx, err)
	}
	return result, nil
}

func (s *Server) acquire(ctx context.Context) bool {
	select {
	case s.sem <- struct{}{}:
		return true
	default:
	}
	if s.cfg.AcquireWait == 0 {
		return false
	}
	timer := time.NewTimer(s.cfg.AcquireWait)
	defer timer.Stop()
	select {
	case s.sem <- struct{}{}:
		return true
	case <-timer.C:
		return false
	case <-ctx.Done():
		return false
	}
}

func (s *Server) release() {
	<-s.sem
}

func (s *Server) logUpstreamError(last *atomic.Int64, message string, args ...any) {
	now := time.Now().UnixNano()
	previous := last.Load()
	if previous != 0 && now-previous < int64(time.Second) {
		return
	}
	if !last.CompareAndSwap(previous, now) {
		return
	}
	s.logger.Error(message, args...)
}

func classifyUpstreamError(ctx context.Context, err error, message string) *S3Error {
	if isTimeout(ctx, err) {
		return errRequestTimeout()
	}
	return errUpstream(message)
}

func classifyTargetCopyError(ctx context.Context, err error) *S3Error {
	if isTimeout(ctx, err) {
		return errRequestTimeout()
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(strings.TrimSpace(apiErr.ErrorCode())) {
		case "nosuchkey", "notfound", "nosuchobject":
			return errNoSuchKey()
		case "nosuchbucket":
			return errNoSuchBucket("")
		case "slowdown", "throttling", "throttlingexception", "toomanyrequestsexception":
			return errSlowDown()
		}
	}
	return errUpstream("target S3 copy failed")
}

func isTimeout(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	if ctx != nil && (errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(ctx.Err(), context.Canceled)) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

func writeCopyObjectResult(w http.ResponseWriter, result *ObjectResult) {
	payload := copyObjectResultXML{LastModified: time.Now().UTC().Format(time.RFC3339)}
	if result != nil {
		payload.ETag = strings.TrimSpace(result.ETag)
		if !result.LastModified.IsZero() {
			payload.LastModified = result.LastModified.UTC().Format(time.RFC3339)
		}
	}
	encoded, err := xml.Marshal(payload)
	if err != nil {
		writeS3Error(w, errInternal("encode CopyObject response failed"))
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	if payload.ETag != "" {
		w.Header().Set("ETag", payload.ETag)
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(encoded)
}
