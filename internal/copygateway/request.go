package copygateway

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type BucketMappings struct {
	identity bool
	targets  map[string]BucketTarget
}

type BucketTarget struct {
	Bucket string `json:"bucket"`
	Region string `json:"region"`
}

type ObjectRef struct {
	Bucket string
	Key    string
	Region string
}

type parsedAddress struct {
	Bucket string
	Key    string
}

func ParseBucketMappings(raw string) (BucketMappings, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return BucketMappings{}, fmt.Errorf("BUCKET_MAPPINGS_JSON is required")
	}
	values := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return BucketMappings{}, fmt.Errorf("parse BUCKET_MAPPINGS_JSON: %w", err)
	}
	mappings := BucketMappings{targets: make(map[string]BucketTarget, len(values))}
	for sourceBucket, rawTarget := range values {
		sourceBucket = strings.TrimSpace(sourceBucket)
		target, err := parseBucketTarget(rawTarget)
		if err != nil {
			return BucketMappings{}, fmt.Errorf("invalid bucket mapping for %q: %w", sourceBucket, err)
		}
		if sourceBucket == "*" {
			if target.Bucket != "*" || target.Region != "" {
				return BucketMappings{}, fmt.Errorf("BUCKET_MAPPINGS_JSON wildcard must map * to *")
			}
			mappings.identity = true
			continue
		}
		if !validBucketMappingName(sourceBucket) || !validBucketMappingName(target.Bucket) {
			return BucketMappings{}, fmt.Errorf("invalid bucket mapping %q -> %q", sourceBucket, target.Bucket)
		}
		mappings.targets[sourceBucket] = target
	}
	if !mappings.identity && len(mappings.targets) == 0 {
		return BucketMappings{}, fmt.Errorf("BUCKET_MAPPINGS_JSON must contain at least one mapping")
	}
	return mappings, nil
}

func parseBucketTarget(raw json.RawMessage) (BucketTarget, error) {
	var bucket string
	if err := json.Unmarshal(raw, &bucket); err == nil {
		return BucketTarget{Bucket: strings.TrimSpace(bucket)}, nil
	}
	var value struct {
		Bucket       string `json:"bucket"`
		TargetBucket string `json:"target_bucket"`
		Region       string `json:"region"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return BucketTarget{}, err
	}
	if strings.TrimSpace(value.Bucket) == "" {
		value.Bucket = value.TargetBucket
	}
	return BucketTarget{Bucket: strings.TrimSpace(value.Bucket), Region: strings.TrimSpace(value.Region)}, nil
}

func validBucketMappingName(bucket string) bool {
	return bucket != "" && !strings.ContainsAny(bucket, " /\\")
}

func (m BucketMappings) Resolve(bucket, key, region string) (ObjectRef, *S3Error) {
	if target, ok := m.targets[bucket]; ok {
		if target.Region != "" {
			region = target.Region
		}
		return ObjectRef{Bucket: target.Bucket, Key: key, Region: region}, nil
	}
	if m.identity {
		return ObjectRef{Bucket: bucket, Key: key, Region: region}, nil
	}
	return ObjectRef{}, errNoSuchBucket(bucket)
}

func (m BucketMappings) ValidateRegions(defaultRegion string) error {
	if m.identity && strings.TrimSpace(defaultRegion) == "" {
		return fmt.Errorf("TARGET_S3_REGION is required for wildcard bucket mapping")
	}
	for sourceBucket, target := range m.targets {
		if strings.TrimSpace(target.Region) == "" && strings.TrimSpace(defaultRegion) == "" {
			return fmt.Errorf("bucket mapping %s requires a region", sourceBucket)
		}
	}
	return nil
}

func parseAddress(r *http.Request, cfg Config) (*parsedAddress, *S3Error) {
	switch cfg.AddressingStyle {
	case addressingPath:
		return parsePathStyle(r)
	case addressingVirtual:
		return parseVirtualHostStyle(r, cfg.PublicS3BaseDomain)
	default:
		if cfg.PublicS3BaseDomain != "" && hostMatchesBaseDomain(r.Host, cfg.PublicS3BaseDomain) {
			return parseVirtualHostStyle(r, cfg.PublicS3BaseDomain)
		}
		return parsePathStyle(r)
	}
}

func parsePathStyle(r *http.Request) (*parsedAddress, *S3Error) {
	escapedPath := r.URL.EscapedPath()
	if escapedPath == "" || escapedPath == "/" {
		return nil, errBadRequest("missing bucket and object key")
	}
	rest := strings.TrimPrefix(escapedPath, "/")
	bucketEscaped, keyEscaped, ok := strings.Cut(rest, "/")
	if !ok || bucketEscaped == "" || keyEscaped == "" {
		return nil, errBadRequest("path-style requests must be /{bucket}/{key}")
	}
	bucket, err := url.PathUnescape(bucketEscaped)
	if err != nil {
		return nil, errBadRequest("invalid bucket path encoding")
	}
	key, err := url.PathUnescape(keyEscaped)
	if err != nil {
		return nil, errBadRequest("invalid object key path encoding")
	}
	return &parsedAddress{Bucket: bucket, Key: key}, nil
}

func parseVirtualHostStyle(r *http.Request, baseDomain string) (*parsedAddress, *S3Error) {
	baseDomain = strings.TrimPrefix(strings.TrimSpace(baseDomain), ".")
	if baseDomain == "" {
		return nil, errBadRequest("PUBLIC_S3_BASE_DOMAIN is required")
	}
	host := hostWithoutPort(r.Host)
	suffix := "." + baseDomain
	if !strings.HasSuffix(host, suffix) || len(host) <= len(suffix) {
		return nil, errBadRequest("host does not match virtual-host-style bucket domain")
	}
	bucket := strings.TrimSuffix(host, suffix)
	keyEscaped := strings.TrimPrefix(r.URL.EscapedPath(), "/")
	if keyEscaped == "" {
		return nil, errBadRequest("virtual-host-style requests must include an object key")
	}
	key, err := url.PathUnescape(keyEscaped)
	if err != nil {
		return nil, errBadRequest("invalid object key path encoding")
	}
	return &parsedAddress{Bucket: bucket, Key: key}, nil
}

func hostMatchesBaseDomain(host, baseDomain string) bool {
	host = hostWithoutPort(host)
	baseDomain = strings.TrimPrefix(strings.TrimSpace(baseDomain), ".")
	return host != baseDomain && strings.HasSuffix(host, "."+baseDomain)
}

func hostWithoutPort(host string) string {
	if value, _, err := net.SplitHostPort(host); err == nil {
		return value
	}
	return strings.TrimSpace(host)
}

func parseCopySourceHeader(raw string) (*parsedAddress, *S3Error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errBadRequest("missing x-amz-copy-source")
	}
	if strings.ContainsAny(raw, "\r\n") {
		return nil, errBadRequest("invalid x-amz-copy-source")
	}
	source := strings.TrimPrefix(raw, "/")
	if strings.Contains(source, "?") {
		return nil, errNotImplemented("CopyObject source versioning is not implemented")
	}
	bucketEscaped, keyEscaped, ok := strings.Cut(source, "/")
	if !ok || bucketEscaped == "" || keyEscaped == "" {
		unescaped, err := url.PathUnescape(source)
		if err != nil {
			return nil, errBadRequest("invalid x-amz-copy-source encoding")
		}
		bucketEscaped, keyEscaped, ok = strings.Cut(unescaped, "/")
		if !ok || bucketEscaped == "" || keyEscaped == "" {
			return nil, errBadRequest("x-amz-copy-source must be /{bucket}/{key}")
		}
		return &parsedAddress{Bucket: bucketEscaped, Key: keyEscaped}, nil
	}
	bucket, err := url.PathUnescape(bucketEscaped)
	if err != nil {
		return nil, errBadRequest("invalid x-amz-copy-source bucket encoding")
	}
	key, err := url.PathUnescape(keyEscaped)
	if err != nil {
		return nil, errBadRequest("invalid x-amz-copy-source key encoding")
	}
	return &parsedAddress{Bucket: bucket, Key: key}, nil
}
