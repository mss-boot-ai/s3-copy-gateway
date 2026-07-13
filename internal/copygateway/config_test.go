package copygateway

import (
	"strings"
	"testing"
	"time"
)

func TestConfigValidatesMinimalRuntime(t *testing.T) {
	cfg := validTestConfig(t)
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestDefaultConfigUsesGenericS3Target(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Target.Provider != "s3" {
		t.Fatalf("target provider = %q, want s3", cfg.Target.Provider)
	}
}

func TestConfigRejectsSourceCheckTimeoutBeyondCopyTimeout(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.SourceCheckTimeout = cfg.CopyTimeout + time.Millisecond
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "SOURCE_CHECK_TIMEOUT") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestConfigRejectsMissingBucketMappings(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.Buckets = BucketMappings{}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "BUCKET_MAPPINGS_JSON") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestConfigAcceptsPerBucketRegionsWithoutGlobalTargetRegion(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.Target.Endpoint = ""
	cfg.Target.Provider = "ovh"
	cfg.Target.Region = ""
	var err error
	cfg.Buckets, err = ParseBucketMappings(`{
		"source-public":{"bucket":"source-cloud","region":"bhs"},
		"dest-public":{"bucket":"dest-cloud","region":"bhs"}
	}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestConfigRejectsDerivedEndpointWithoutAnyBucketRegion(t *testing.T) {
	cfg := validTestConfig(t)
	cfg.Target.Endpoint = ""
	cfg.Target.Provider = "ovh"
	cfg.Target.Region = ""
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "requires a region") {
		t.Fatalf("Validate error = %v", err)
	}
}

func TestLoadConfigUsesEnvironmentOnly(t *testing.T) {
	t.Setenv("SOURCE_S3_ENDPOINT", "http://source.local:9000")
	t.Setenv("SOURCE_S3_ACCESS_KEY", "source-ak")
	t.Setenv("SOURCE_S3_SECRET_KEY", "source-sk")
	t.Setenv("TARGET_S3_ENDPOINT", "https://target.example")
	t.Setenv("TARGET_S3_ACCESS_KEY", "target-ak")
	t.Setenv("TARGET_S3_SECRET_KEY", "target-sk")
	t.Setenv("TARGET_S3_REGION", "bhs")
	t.Setenv("BUCKET_MAPPINGS_JSON", `{"source-public":"source-cloud","dest-public":"dest-cloud"}`)
	t.Setenv("MAX_IN_FLIGHT", "512")

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	if cfg.MaxInFlight != 512 || cfg.Source.Endpoint != "http://source.local:9000" || cfg.Target.Region != "bhs" {
		t.Fatalf("config = %+v", cfg)
	}
	got, s3err := cfg.Buckets.Resolve("source-public", "path/file", cfg.Target.Region)
	if s3err != nil || got.Bucket != "source-cloud" || got.Key != "path/file" {
		t.Fatalf("mapped source = %+v, error = %v", got, s3err)
	}
	if cfg.Auth.AccessKey != "source-ak" || cfg.Auth.SecretKey != "source-sk" {
		t.Fatalf("auth should default to source credentials: %+v", cfg.Auth)
	}
}

func validTestConfig(t *testing.T) Config {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Auth.AccessKey = authTestAccessKey
	cfg.Auth.SecretKey = authTestSecretKey
	cfg.Auth.AcceptRegions = []string{authTestRegion}
	cfg.Source.Endpoint = "http://source.local:9000"
	cfg.Source.AccessKey = "source-ak"
	cfg.Source.SecretKey = "source-sk"
	cfg.Target.Endpoint = "https://target.example"
	cfg.Target.Region = "bhs"
	cfg.Target.AccessKey = "target-ak"
	cfg.Target.SecretKey = "target-sk"
	mappings, err := ParseBucketMappings(`{"source-public":"source-cloud","dest-public":"dest-cloud"}`)
	if err != nil {
		t.Fatalf("ParseBucketMappings: %v", err)
	}
	cfg.Buckets = mappings
	return cfg
}
