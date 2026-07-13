package copygateway

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

func TestGatewayFallsBackFromSource404ToMappedTargetCopy(t *testing.T) {
	var mu sync.Mutex
	var sourceMethod, sourcePath, targetMethod, targetPath, targetCopySource string
	sourceUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sourceMethod, sourcePath = r.Method, r.URL.EscapedPath()
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
	}))
	defer sourceUpstream.Close()
	targetUpstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		targetMethod, targetPath = r.Method, r.URL.EscapedPath()
		targetCopySource = r.Header.Get(copySourceHeader)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>&quot;cloud-etag&quot;</ETag><LastModified>2026-07-10T10:00:00Z</LastModified></CopyObjectResult>`)
	}))
	defer targetUpstream.Close()

	cfg := validTestConfig(t)
	cfg.Source = testS3Config(sourceUpstream.URL)
	cfg.Source.Region = authTestRegion
	cfg.Target = testS3Config(targetUpstream.URL)
	cfg.Target.Region = "bhs"
	source, err := NewS3Source(cfg.Source, cfg.MaxInFlight)
	if err != nil {
		t.Fatalf("NewS3Source: %v", err)
	}
	target, err := NewS3CopyTarget(cfg.Target, cfg.MaxInFlight)
	if err != nil {
		t.Fatalf("NewS3CopyTarget: %v", err)
	}
	handler, err := NewServer(cfg, source, target, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	gateway := httptest.NewServer(handler)
	defer gateway.Close()

	client := s3.New(s3.Options{
		BaseEndpoint: aws.String(gateway.URL),
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
		Key:        aws.String("out/path.txt"),
		CopySource: aws.String("source-public/in/path.txt"),
	})
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if out.CopyObjectResult == nil || aws.ToString(out.CopyObjectResult.ETag) != `"cloud-etag"` {
		t.Fatalf("result = %+v", out.CopyObjectResult)
	}

	mu.Lock()
	defer mu.Unlock()
	if sourceMethod != http.MethodHead || sourcePath != "/source-public/in/path.txt" {
		t.Fatalf("source request = %s %s", sourceMethod, sourcePath)
	}
	if targetMethod != http.MethodPut || targetPath != "/dest-cloud/out/path.txt" {
		t.Fatalf("target request = %s %s", targetMethod, targetPath)
	}
	if targetCopySource != "source-cloud/in/path.txt" {
		t.Fatalf("target x-amz-copy-source = %q", targetCopySource)
	}
}
