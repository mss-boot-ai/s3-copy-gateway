package copygateway

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

var providerEndpointTemplates = map[string]string{
	"ovh": "https://s3.%s.io.cloud.ovh.net",
}

type ObjectResult struct {
	ETag         string
	LastModified time.Time
}

type CopyObjectRequest struct {
	Source ObjectRef
	Target ObjectRef
}

type SourceChecker interface {
	HeadObject(context.Context, ObjectRef) (*ObjectResult, error)
}

type CopyTarget interface {
	CopyObject(context.Context, CopyObjectRequest) (*ObjectResult, error)
}

type S3Source struct {
	client *s3.Client
}

type S3CopyTarget struct {
	cfg     S3Config
	client  *http.Client
	clients sync.Map
}

func NewS3Source(cfg S3Config, maxInFlight int) (*S3Source, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, fmt.Errorf("source S3 endpoint is required")
	}
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, fmt.Errorf("source S3 credentials are required")
	}
	client := newS3Client(cfg, sourceEndpoint(cfg.Endpoint), cfg.Region, newHTTPClient(cfg, maxInFlight))
	return &S3Source{client: client}, nil
}

func NewS3CopyTarget(cfg S3Config, maxInFlight int) (*S3CopyTarget, error) {
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return nil, fmt.Errorf("target S3 credentials are required")
	}
	return &S3CopyTarget{
		cfg:    cfg,
		client: newHTTPClient(cfg, maxInFlight),
	}, nil
}

func (s *S3Source) HeadObject(ctx context.Context, object ObjectRef) (*ObjectResult, error) {
	out, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(object.Bucket),
		Key:    aws.String(object.Key),
	})
	if err != nil {
		return nil, err
	}
	result := &ObjectResult{ETag: aws.ToString(out.ETag)}
	if out.LastModified != nil {
		result.LastModified = *out.LastModified
	}
	return result, nil
}

func (t *S3CopyTarget) CopyObject(ctx context.Context, req CopyObjectRequest) (*ObjectResult, error) {
	out, err := t.clientFor(req.Target.Region).CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:            aws.String(req.Target.Bucket),
		Key:               aws.String(req.Target.Key),
		CopySource:        aws.String(encodeCopySource(req.Source.Bucket, req.Source.Key)),
		MetadataDirective: types.MetadataDirectiveCopy,
		TaggingDirective:  types.TaggingDirectiveCopy,
	})
	if err != nil {
		return nil, err
	}
	if out.CopyObjectResult == nil {
		return &ObjectResult{}, nil
	}
	result := &ObjectResult{ETag: aws.ToString(out.CopyObjectResult.ETag)}
	if out.CopyObjectResult.LastModified != nil {
		result.LastModified = *out.CopyObjectResult.LastModified
	}
	return result, nil
}

func (t *S3CopyTarget) clientFor(region string) *s3.Client {
	region = strings.TrimSpace(region)
	if region == "" {
		region = strings.TrimSpace(t.cfg.Region)
	}
	if region == "" {
		region = "auto"
	}

	if client, ok := t.clients.Load(region); ok {
		return client.(*s3.Client)
	}
	client := newS3Client(t.cfg, targetEndpoint(t.cfg, region), region, t.client)
	actual, _ := t.clients.LoadOrStore(region, client)
	return actual.(*s3.Client)
}

func newS3Client(cfg S3Config, endpoint, region string, httpClient *http.Client) *s3.Client {
	if strings.TrimSpace(region) == "" {
		region = "us-east-1"
	}
	options := s3.Options{
		Region:                     region,
		Credentials:                staticCredentials(cfg),
		HTTPClient:                 httpClient,
		UsePathStyle:               cfg.PathStyle,
		Retryer:                    aws.NopRetryer{},
		RetryMaxAttempts:           1,
		RequestChecksumCalculation: parseRequestChecksumCalculation(cfg.RequestChecksumCalculation),
		ResponseChecksumValidation: parseResponseChecksumValidation(cfg.ResponseChecksumValidation),
	}
	if endpoint != "" {
		options.BaseEndpoint = aws.String(endpoint)
	}
	return s3.New(options)
}

func staticCredentials(cfg S3Config) aws.CredentialsProvider {
	return aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: cfg.AccessKey, SecretAccessKey: cfg.SecretKey}, nil
	})
}

func newHTTPClient(cfg S3Config, maxInFlight int) *http.Client {
	if maxInFlight <= 0 {
		maxInFlight = 256
	}
	return &http.Client{Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxConnsPerHost:       maxInFlight,
		MaxIdleConns:          maxInFlight * 2,
		MaxIdleConnsPerHost:   maxInFlight,
		IdleConnTimeout:       cfg.IdleConnTimeout,
		TLSHandshakeTimeout:   cfg.TLSHandshakeTimeout,
		ResponseHeaderTimeout: cfg.ResponseHeaderTimeout,
		ExpectContinueTimeout: time.Second,
		DialContext: (&net.Dialer{
			Timeout: cfg.DialTimeout,
		}).DialContext,
	}}
}

func sourceEndpoint(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return "http://" + endpoint
}

func targetEndpoint(cfg S3Config, region string) string {
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		template := providerEndpointTemplates[strings.ToLower(strings.TrimSpace(cfg.Provider))]
		if strings.Contains(template, "%s") {
			endpoint = fmt.Sprintf(template, region)
		} else {
			endpoint = template
		}
	}
	if endpoint == "" || strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	return "https://" + endpoint
}

func encodeCopySource(bucket, objectKey string) string {
	segments := strings.Split(objectKey, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return url.PathEscape(bucket) + "/" + strings.Join(segments, "/")
}

func isObjectNotFound(err error) bool {
	if err == nil {
		return false
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch strings.ToLower(strings.TrimSpace(apiErr.ErrorCode())) {
		case "nosuchkey", "nosuchbucket", "notfound", "nosuchobject":
			return true
		}
	}
	var statusErr interface{ HTTPStatusCode() int }
	return errors.As(err, &statusErr) && statusErr.HTTPStatusCode() == http.StatusNotFound
}

func parseRequestChecksumCalculation(value string) aws.RequestChecksumCalculation {
	if strings.EqualFold(strings.TrimSpace(value), "WHEN_SUPPORTED") {
		return aws.RequestChecksumCalculationWhenSupported
	}
	return aws.RequestChecksumCalculationWhenRequired
}

func parseResponseChecksumValidation(value string) aws.ResponseChecksumValidation {
	if strings.EqualFold(strings.TrimSpace(value), "WHEN_SUPPORTED") {
		return aws.ResponseChecksumValidationWhenSupported
	}
	return aws.ResponseChecksumValidationWhenRequired
}
