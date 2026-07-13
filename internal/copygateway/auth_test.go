package copygateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

const (
	authTestAccessKey = "copy-ak"
	authTestSecretKey = "copy-sk"
	authTestRegion    = "us-east-1"
)

var authTestNow = time.Date(2026, 7, 10, 10, 0, 0, 0, time.UTC)

func TestAuthenticatorVerifiesAWSV4SignedRequest(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)

	got, s3err := auth.Verify(req)
	if s3err != nil {
		t.Fatalf("Verify returned error: %v", s3err)
	}
	if got.AccessKey != authTestAccessKey || got.Region != authTestRegion {
		t.Fatalf("auth result = %+v", got)
	}
}

func TestAuthenticatorSupportsMultipleCredentials(t *testing.T) {
	auth, err := NewAuthenticator(AuthConfig{
		AccessKey:       authTestAccessKey,
		SecretKey:       authTestSecretKey,
		CredentialsJSON: `{"second-ak":"second-sk"}`,
		AcceptRegions:   []string{authTestRegion},
		ClockSkew:       5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	auth.now = func() time.Time { return authTestNow }
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, "second-ak", "second-sk", authTestRegion, authTestNow)

	got, s3err := auth.Verify(req)
	if s3err != nil {
		t.Fatalf("Verify returned error: %v", s3err)
	}
	if got.AccessKey != "second-ak" {
		t.Fatalf("access key = %q, want second-ak", got.AccessKey)
	}
}

func TestAuthenticatorRejectsInvalidAccessKey(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, "wrong-ak", "wrong-sk", authTestRegion, authTestNow)

	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "InvalidAccessKeyId")
}

func TestAuthenticatorRejectsInvalidSignature(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)
	authorization := req.Header.Get("Authorization")
	last := authorization[len(authorization)-1]
	replacement := byte('0')
	if last == replacement {
		replacement = '1'
	}
	req.Header.Set("Authorization", authorization[:len(authorization)-1]+string(replacement))

	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "SignatureDoesNotMatch")
}

func TestAuthenticatorRejectsClockSkew(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow.Add(-10*time.Minute))

	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "RequestTimeTooSkewed")
}

func TestAuthenticatorRejectsUnacceptedRegion(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", nil, authTestAccessKey, authTestSecretKey, "eu-west-1", authTestNow)

	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "SignatureDoesNotMatch")
}

func TestAuthenticatorPreservesDoubleSlashInCanonicalURI(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/a//b.txt", nil, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)

	if _, s3err := auth.Verify(req); s3err != nil {
		t.Fatalf("Verify returned error: %v", s3err)
	}
}

func TestAuthenticatorVerifiesCanonicalQueryEdges(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt?z=last&empty=&space=a%20b&space=a+b&slash=a%2Fb&dup=2&dup=1", nil, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)

	if _, s3err := auth.Verify(req); s3err != nil {
		t.Fatalf("Verify returned error: %v", s3err)
	}
}

func TestAuthenticatorVerifiesSignedCopySourceHeader(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := authSignedRequest(t, "http://s3.local/target/object.txt", map[string]string{
		"x-amz-copy-source": "/source/a%2Fb.txt",
	}, authTestAccessKey, authTestSecretKey, authTestRegion, authTestNow)
	if !strings.Contains(req.Header.Get("Authorization"), "x-amz-copy-source") {
		t.Fatalf("Authorization SignedHeaders do not include x-amz-copy-source: %s", req.Header.Get("Authorization"))
	}

	if _, s3err := auth.Verify(req); s3err != nil {
		t.Fatalf("Verify returned error: %v", s3err)
	}

	req.Header.Set("x-amz-copy-source", "/source/tampered.txt")
	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "SignatureDoesNotMatch")
}

func TestAuthenticatorRejectsUnsignedCopySourceHeader(t *testing.T) {
	auth := newAuthTestAuthenticator(t)
	req := httptest.NewRequest(http.MethodPut, "http://s3.local/target/object.txt", http.NoBody)
	req.Header.Set(payloadHashHeader, payloadUnsigned)
	req.Header.Set(amzDateHeader, authTestNow.UTC().Format("20060102T150405Z"))
	signer := v4.NewSigner()
	if err := signer.SignHTTP(context.Background(), aws.Credentials{
		AccessKeyID:     authTestAccessKey,
		SecretAccessKey: authTestSecretKey,
	}, req, payloadUnsigned, sigV4Service, authTestRegion, authTestNow); err != nil {
		t.Fatalf("SignHTTP: %v", err)
	}
	req.Header.Set(copySourceHeader, "/source/object.txt")

	_, s3err := auth.Verify(req)
	assertAuthErrorCode(t, s3err, "SignatureDoesNotMatch")
}

func newAuthTestAuthenticator(t *testing.T) *Authenticator {
	t.Helper()
	auth, err := NewAuthenticator(AuthConfig{
		AccessKey:     authTestAccessKey,
		SecretKey:     authTestSecretKey,
		AcceptRegions: []string{authTestRegion},
		ClockSkew:     5 * time.Minute,
	})
	if err != nil {
		t.Fatalf("NewAuthenticator: %v", err)
	}
	auth.now = func() time.Time { return authTestNow }
	return auth
}

func authSignedRequest(t *testing.T, rawURL string, headers map[string]string, accessKey, secretKey, region string, now time.Time) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, rawURL, http.NoBody)
	req.Header.Set(payloadHashHeader, payloadUnsigned)
	req.Header.Set(amzDateHeader, now.UTC().Format("20060102T150405Z"))
	req.Header.Set(copySourceHeader, "/source/object.txt")
	for key, value := range headers {
		req.Header.Set(key, value)
	}
	signer := v4.NewSigner()
	if err := signer.SignHTTP(context.Background(), aws.Credentials{
		AccessKeyID:     accessKey,
		SecretAccessKey: secretKey,
	}, req, payloadUnsigned, sigV4Service, region, now); err != nil {
		t.Fatalf("SignHTTP: %v", err)
	}
	return req
}

func assertAuthErrorCode(t *testing.T, s3err *S3Error, want string) {
	t.Helper()
	if s3err == nil || s3err.Code != want {
		t.Fatalf("Verify error = %#v, want %s", s3err, want)
	}
}
