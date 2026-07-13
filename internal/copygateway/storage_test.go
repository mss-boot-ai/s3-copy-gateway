package copygateway

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/smithy-go"
)

func TestIsObjectNotFoundUsesStructuredErrorCode(t *testing.T) {
	if !isObjectNotFound(&smithy.GenericAPIError{Code: "NoSuchKey", Message: "missing"}) {
		t.Fatal("NoSuchKey should be classified as missing")
	}
	if !isObjectNotFound(&smithy.GenericAPIError{Code: "NoSuchBucket", Message: "missing"}) {
		t.Fatal("NoSuchBucket should be classified as missing")
	}
	if isObjectNotFound(&smithy.GenericAPIError{Code: "AccessDenied", Message: "denied"}) {
		t.Fatal("AccessDenied must not be classified as missing")
	}
}

func TestS3SourceHead404IsClassifiedAsMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead || r.URL.Path != "/source-public/path/object.txt" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`))
	}))
	defer server.Close()

	cfg := testS3Config(server.URL)
	source, err := NewS3Source(cfg, 4)
	if err != nil {
		t.Fatalf("NewS3Source: %v", err)
	}
	_, err = source.HeadObject(context.Background(), ObjectRef{Bucket: "source-public", Key: "path/object.txt"})
	if err == nil || !isObjectNotFound(err) {
		t.Fatalf("HeadObject error = %v", err)
	}
}

func TestS3CopyTargetSendsMappedCopySource(t *testing.T) {
	var gotCopySource string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/dest-cloud/out/file.txt" {
			t.Errorf("request = %s %s", r.Method, r.URL.Path)
		}
		gotCopySource = r.Header.Get(copySourceHeader)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `<CopyObjectResult><ETag>&quot;copied&quot;</ETag><LastModified>2026-07-10T10:00:00Z</LastModified></CopyObjectResult>`)
	}))
	defer server.Close()

	cfg := testS3Config(server.URL)
	target, err := NewS3CopyTarget(cfg, 4)
	if err != nil {
		t.Fatalf("NewS3CopyTarget: %v", err)
	}
	result, err := target.CopyObject(context.Background(), CopyObjectRequest{
		Source: ObjectRef{Bucket: "source-cloud", Key: "a b//c.txt", Region: "bhs"},
		Target: ObjectRef{Bucket: "dest-cloud", Key: "out/file.txt", Region: "bhs"},
	})
	if err != nil {
		t.Fatalf("CopyObject: %v", err)
	}
	if gotCopySource != "source-cloud/a%20b//c.txt" {
		t.Fatalf("x-amz-copy-source = %q", gotCopySource)
	}
	if result.ETag != `"copied"` || result.LastModified.IsZero() {
		t.Fatalf("result = %+v", result)
	}
}

func testS3Config(endpoint string) S3Config {
	return S3Config{
		Endpoint:                   endpoint,
		Region:                     "bhs",
		AccessKey:                  "ak",
		SecretKey:                  "sk",
		PathStyle:                  true,
		DialTimeout:                time.Second,
		TLSHandshakeTimeout:        time.Second,
		ResponseHeaderTimeout:      time.Second,
		IdleConnTimeout:            time.Second,
		RequestChecksumCalculation: "WHEN_REQUIRED",
		ResponseChecksumValidation: "WHEN_REQUIRED",
	}
}

func TestEncodeCopySourcePreservesEmptyPathSegments(t *testing.T) {
	if got := encodeCopySource("bucket", "a b//c.txt"); !strings.EqualFold(got, "bucket/a%20b//c.txt") {
		t.Fatalf("encodeCopySource = %q", got)
	}
}
