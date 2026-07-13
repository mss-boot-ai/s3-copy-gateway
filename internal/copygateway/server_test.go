package copygateway

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
)

type fakeSourceChecker struct {
	mu     sync.Mutex
	result *ObjectResult
	err    error
	calls  []ObjectRef
	head   func(context.Context, ObjectRef) (*ObjectResult, error)
}

func (f *fakeSourceChecker) HeadObject(ctx context.Context, object ObjectRef) (*ObjectResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, object)
	head := f.head
	result := f.result
	err := f.err
	f.mu.Unlock()
	if head != nil {
		return head(ctx, object)
	}
	return result, err
}

func (f *fakeSourceChecker) snapshot() []ObjectRef {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ObjectRef(nil), f.calls...)
}

type fakeCopyTarget struct {
	mu     sync.Mutex
	result *ObjectResult
	err    error
	calls  []CopyObjectRequest
}

func (f *fakeCopyTarget) CopyObject(_ context.Context, req CopyObjectRequest) (*ObjectResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return f.result, f.err
}

func (f *fakeCopyTarget) snapshot() []CopyObjectRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]CopyObjectRequest(nil), f.calls...)
}

func TestServerReturnsSuccessWithoutCloudCopyWhenSourceExists(t *testing.T) {
	source := &fakeSourceChecker{result: &ObjectResult{
		ETag:         `"local-etag"`,
		LastModified: authTestNow,
	}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, validTestConfig(t), source, target)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("ETag") != `"local-etag"` || !strings.Contains(rec.Body.String(), "<CopyObjectResult>") {
		t.Fatalf("headers = %v, body = %s", rec.Header(), rec.Body.String())
	}
	if calls := source.snapshot(); len(calls) != 1 || calls[0].Bucket != "source-public" || calls[0].Key != "in.txt" {
		t.Fatalf("source calls = %+v", calls)
	}
	if calls := target.snapshot(); len(calls) != 0 {
		t.Fatalf("cloud copy calls = %+v", calls)
	}
}

func TestServerAcceptsAWSSDKCopyObject(t *testing.T) {
	cfg := validTestConfig(t)
	source := &fakeSourceChecker{result: &ObjectResult{ETag: `"local-etag"`, LastModified: time.Now().UTC()}}
	target := &fakeCopyTarget{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	handler, err := NewServer(cfg, source, target, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	var payloadHash string
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payloadHash = r.Header.Get(payloadHashHeader)
		handler.ServeHTTP(w, r)
	}))
	defer httpServer.Close()
	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(httpServer.URL),
		Region:       authTestRegion,
		Credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
			return aws.Credentials{AccessKeyID: authTestAccessKey, SecretAccessKey: authTestSecretKey}, nil
		}),
		UsePathStyle:     true,
		Retryer:          aws.NopRetryer{},
		RetryMaxAttempts: 1,
	})
	out, err := client.CopyObject(context.Background(), &s3.CopyObjectInput{
		Bucket:     aws.String("dest-public"),
		Key:        aws.String("out.txt"),
		CopySource: aws.String("source-public/in.txt"),
	})
	if err != nil {
		t.Fatalf("AWS SDK CopyObject: %v", err)
	}
	if out.CopyObjectResult == nil || aws.ToString(out.CopyObjectResult.ETag) != `"local-etag"` {
		t.Fatalf("result = %+v", out.CopyObjectResult)
	}
	if payloadHash == "" {
		t.Fatal("AWS SDK request did not include x-amz-content-sha256")
	}
	if len(source.snapshot()) != 1 || len(target.snapshot()) != 0 {
		t.Fatalf("source calls = %+v, target calls = %+v", source.snapshot(), target.snapshot())
	}
}

func TestServerCopiesMappedCloudObjectsWhenSourceIsMissing(t *testing.T) {
	source := &fakeSourceChecker{err: &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}}
	target := &fakeCopyTarget{result: &ObjectResult{ETag: `"cloud-etag"`, LastModified: authTestNow}}
	server := newServerTest(t, validTestConfig(t), source, target)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/path/out.txt", "/source-public/path/in.txt"))

	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"cloud-etag"` {
		t.Fatalf("status = %d, headers = %v, body = %s", rec.Code, rec.Header(), rec.Body.String())
	}
	if calls := source.snapshot(); len(calls) != 1 || calls[0].Bucket != "source-public" || calls[0].Key != "path/in.txt" {
		t.Fatalf("source calls = %+v", calls)
	}
	calls := target.snapshot()
	if len(calls) != 1 {
		t.Fatalf("cloud copy calls = %+v", calls)
	}
	if calls[0].Source != (ObjectRef{Bucket: "source-cloud", Key: "path/in.txt", Region: "bhs"}) {
		t.Fatalf("cloud source = %+v", calls[0].Source)
	}
	if calls[0].Target != (ObjectRef{Bucket: "dest-cloud", Key: "path/out.txt", Region: "bhs"}) {
		t.Fatalf("cloud target = %+v", calls[0].Target)
	}
}

func TestServerDoesNotFallbackOnSourceFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "access denied", err: &smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}, wantStatus: http.StatusBadGateway},
		{name: "source internal error", err: &smithy.GenericAPIError{Code: "InternalError", Message: "failed"}, wantStatus: http.StatusBadGateway},
		{name: "network error", err: io.ErrUnexpectedEOF, wantStatus: http.StatusBadGateway},
		{name: "timeout", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSourceChecker{err: tt.err}
			target := &fakeCopyTarget{}
			server := newServerTest(t, validTestConfig(t), source, target)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt"))
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if calls := target.snapshot(); len(calls) != 0 {
				t.Fatalf("cloud copy must not run: %+v", calls)
			}
		})
	}
}

func TestServerBoundsSourceCheckDuration(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.SourceCheckTimeout = 10 * time.Millisecond
	source := &fakeSourceChecker{head: func(ctx context.Context, _ ObjectRef) (*ObjectResult, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, cfg, source, target)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt"))
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(target.snapshot()) != 0 {
		t.Fatal("cloud copy must not run after source timeout")
	}
}

func TestServerMapsTargetCopyFailures(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantRetry  bool
	}{
		{name: "missing", err: &smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}, wantStatus: http.StatusNotFound, wantCode: "NoSuchKey"},
		{name: "throttled", err: &smithy.GenericAPIError{Code: "SlowDown", Message: "slow"}, wantStatus: http.StatusServiceUnavailable, wantCode: "SlowDown", wantRetry: true},
		{name: "timeout", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantCode: "RequestTimeout"},
		{name: "other", err: errors.New("failed"), wantStatus: http.StatusBadGateway, wantCode: "InternalError"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeSourceChecker{err: &smithy.GenericAPIError{Code: "NotFound", Message: "missing"}}
			target := &fakeCopyTarget{err: tt.err}
			server := newServerTest(t, validTestConfig(t), source, target)
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt"))
			if rec.Code != tt.wantStatus || !strings.Contains(rec.Body.String(), "<Code>"+tt.wantCode+"</Code>") {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if got := rec.Header().Get("Retry-After"); (got != "") != tt.wantRetry {
				t.Fatalf("Retry-After = %q", got)
			}
		})
	}
}

func TestServerRejectsTamperedCopySourceBeforeUpstreamCalls(t *testing.T) {
	source := &fakeSourceChecker{result: &ObjectResult{}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, validTestConfig(t), source, target)
	req := signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt")
	req.Header.Set(copySourceHeader, "/source-public/tampered.txt")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "SignatureDoesNotMatch") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(source.snapshot()) != 0 || len(target.snapshot()) != 0 {
		t.Fatal("upstream calls must not run after auth failure")
	}
}

func TestServerRejectsUnmappedBucketsBeforeUpstreamCalls(t *testing.T) {
	source := &fakeSourceChecker{result: &ObjectResult{}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, validTestConfig(t), source, target)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/unknown/in.txt"))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "NoSuchBucket") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(source.snapshot()) != 0 || len(target.snapshot()) != 0 {
		t.Fatal("upstream calls must not run for unmapped buckets")
	}
}

func TestServerRejectsCrossRegionCopyBeforeUpstreamCalls(t *testing.T) {
	cfg := validTestConfig(t)
	var err error
	cfg.Buckets, err = ParseBucketMappings(`{
		"source-public":{"bucket":"source-cloud","region":"bhs"},
		"dest-public":{"bucket":"dest-cloud","region":"de"}
	}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	source := &fakeSourceChecker{result: &ObjectResult{}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, cfg, source, target)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, signedCopyRequest(t, "/dest-public/out.txt", "/source-public/in.txt"))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "cross-region") {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(source.snapshot()) != 0 || len(target.snapshot()) != 0 {
		t.Fatal("upstream calls must not run for cross-region copy")
	}
}

func TestServerRejectsBusyCopyWithoutExceedingCapacity(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.MaxInFlight = 1
	cfg.AcquireWait = 10 * time.Millisecond
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	source := &fakeSourceChecker{head: func(ctx context.Context, _ ObjectRef) (*ObjectResult, error) {
		started <- struct{}{}
		select {
		case <-release:
			return &ObjectResult{ETag: `"ok"`}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}
	target := &fakeCopyTarget{}
	server := newServerTest(t, cfg, source, target)
	firstReq := signedCopyRequest(t, "/dest-public/first.txt", "/source-public/first.txt")
	secondReq := signedCopyRequest(t, "/dest-public/second.txt", "/source-public/second.txt")
	firstRec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		server.ServeHTTP(firstRec, firstReq)
		close(done)
	}()
	<-started

	secondRec := httptest.NewRecorder()
	server.ServeHTTP(secondRec, secondReq)
	if secondRec.Code != http.StatusServiceUnavailable || secondRec.Header().Get("Retry-After") != "5" {
		t.Fatalf("second status = %d, headers = %v, body = %s", secondRec.Code, secondRec.Header(), secondRec.Body.String())
	}
	close(release)
	<-done
	if firstRec.Code != http.StatusOK {
		t.Fatalf("first status = %d, body = %s", firstRec.Code, firstRec.Body.String())
	}
}

func TestServerExposesHealthAndOnlyImplementsCopy(t *testing.T) {
	server := newServerTest(t, validTestConfig(t), &fakeSourceChecker{}, &fakeCopyTarget{})
	health := httptest.NewRecorder()
	server.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d", health.Code)
	}

	put := httptest.NewRecorder()
	server.ServeHTTP(put, httptest.NewRequest(http.MethodPut, "/dest-public/object.txt", http.NoBody))
	if put.Code != http.StatusNotImplemented {
		t.Fatalf("put status = %d, body = %s", put.Code, put.Body.String())
	}
}

func TestServerRejectsUnsupportedCopySemantics(t *testing.T) {
	server := newServerTest(t, validTestConfig(t), &fakeSourceChecker{}, &fakeCopyTarget{})
	for _, header := range []string{
		"x-amz-copy-source-if-match",
		"x-amz-acl",
		"x-amz-server-side-encryption",
		"x-amz-storage-class",
	} {
		t.Run(header, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPut, "/dest-public/object.txt", http.NoBody)
			req.Header.Set(copySourceHeader, "/source-public/object.txt")
			req.Header.Set(header, "value")
			rec := httptest.NewRecorder()
			server.ServeHTTP(rec, req)
			if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "NotImplemented") {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func newServerTest(t *testing.T, cfg Config, source SourceChecker, target CopyTarget) *Server {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := NewServer(cfg, source, target, logger)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	server.auth.now = func() time.Time { return authTestNow }
	return server
}

func signedCopyRequest(t *testing.T, targetPath, source string) *http.Request {
	t.Helper()
	return authSignedRequest(t, "http://s3.local"+targetPath, map[string]string{
		copySourceHeader: source,
	}, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)
}
