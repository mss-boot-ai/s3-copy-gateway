package copygateway

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBucketMappingsMapBothCloudBuckets(t *testing.T) {
	mappings, err := ParseBucketMappings(`{"source-public":"source-cloud","dest-public":"dest-cloud"}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	source, s3err := mappings.Resolve("source-public", "a/b.txt", "bhs")
	if s3err != nil || source != (ObjectRef{Bucket: "source-cloud", Key: "a/b.txt", Region: "bhs"}) {
		t.Fatalf("source = %+v, error = %v", source, s3err)
	}
	if _, s3err := mappings.Resolve("unknown", "a", "bhs"); s3err == nil || s3err.Code != "NoSuchBucket" {
		t.Fatalf("unknown bucket error = %#v", s3err)
	}
}

func TestBucketMappingsCarryPerBucketOVHRegion(t *testing.T) {
	mappings, err := ParseBucketMappings(`{
		"source-public":{"bucket":"source-cloud","region":"bhs"},
		"dest-public":{"target_bucket":"dest-cloud","region":"de"}
	}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	source, s3err := mappings.Resolve("source-public", "a.txt", "")
	if s3err != nil || source.Bucket != "source-cloud" || source.Region != "bhs" {
		t.Fatalf("source = %+v, error = %v", source, s3err)
	}
	target, s3err := mappings.Resolve("dest-public", "b.txt", "")
	if s3err != nil || target.Bucket != "dest-cloud" || target.Region != "de" {
		t.Fatalf("target = %+v, error = %v", target, s3err)
	}
}

func TestBucketMappingsSupportExplicitIdentityWildcard(t *testing.T) {
	mappings, err := ParseBucketMappings(`{"*":"*"}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	got, s3err := mappings.Resolve("any-bucket", "key", "de")
	if s3err != nil || got.Bucket != "any-bucket" {
		t.Fatalf("Resolve = %+v, error = %v", got, s3err)
	}
}

func TestParsePathStylePreservesDoubleSlash(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "http://s3.local/source-public/a//b.txt", http.NoBody)
	got, s3err := parsePathStyle(req)
	if s3err != nil {
		t.Fatalf("parsePathStyle: %v", s3err)
	}
	if got.Bucket != "source-public" || got.Key != "a//b.txt" {
		t.Fatalf("address = %+v", got)
	}
}

func TestParseVirtualHostStylePreservesDoubleSlash(t *testing.T) {
	req := httptest.NewRequest(http.MethodPut, "https://dest-public.s3.local/a//b.txt", http.NoBody)
	req.Host = "dest-public.s3.local:443"
	got, s3err := parseVirtualHostStyle(req, "s3.local")
	if s3err != nil {
		t.Fatalf("parseVirtualHostStyle: %v", s3err)
	}
	if got.Bucket != "dest-public" || got.Key != "a//b.txt" {
		t.Fatalf("address = %+v", got)
	}
}

func TestParseCopySourceHeaderDecodesEscapedKey(t *testing.T) {
	got, s3err := parseCopySourceHeader("/source-public/a%2Fb%20c.txt")
	if s3err != nil {
		t.Fatalf("parseCopySourceHeader: %v", s3err)
	}
	if got.Bucket != "source-public" || got.Key != "a/b c.txt" {
		t.Fatalf("address = %+v", got)
	}
}
