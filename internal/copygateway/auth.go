package copygateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const (
	sigV4Algorithm = "AWS4-HMAC-SHA256"
	sigV4Service   = "s3"
	sigV4Terminal  = "aws4_request"

	amzDateHeader     = "x-amz-date"
	payloadHashHeader = "x-amz-content-sha256"

	payloadUnsigned  = "UNSIGNED-PAYLOAD"
	payloadStreaming = "STREAMING-AWS4-HMAC-SHA256-PAYLOAD"
)

type Credential struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

type Authenticator struct {
	secrets       map[string]string
	acceptAll     bool
	acceptRegions map[string]struct{}
	clockSkew     time.Duration
	now           func() time.Time
}

type AuthResult struct {
	AccessKey string
	Region    string
}

type parsedAuthorization struct {
	AccessKey     string
	Date          string
	Region        string
	Service       string
	Terminal      string
	SignedHeaders []string
	Signature     string
}

func NewAuthenticator(cfg AuthConfig) (*Authenticator, error) {
	secrets := make(map[string]string)
	credentials, err := cfg.CredentialsStrict()
	if err != nil {
		return nil, err
	}
	for _, cred := range credentials {
		secrets[cred.AccessKey] = cred.SecretKey
	}
	if len(secrets) == 0 {
		return nil, fmt.Errorf("missing S3 credentials")
	}
	regions := make(map[string]struct{})
	acceptAll := false
	for _, region := range cfg.AcceptRegions {
		region = strings.TrimSpace(region)
		if region == "" {
			continue
		}
		if region == "*" {
			acceptAll = true
		}
		regions[region] = struct{}{}
	}
	if len(regions) == 0 {
		acceptAll = true
	}
	if cfg.ClockSkew <= 0 {
		cfg.ClockSkew = 5 * time.Minute
	}
	return &Authenticator{
		secrets:       secrets,
		acceptAll:     acceptAll,
		acceptRegions: regions,
		clockSkew:     cfg.ClockSkew,
		now:           time.Now,
	}, nil
}

func (a *Authenticator) Verify(r *http.Request) (*AuthResult, *S3Error) {
	parsed, err := parseAuthorizationHeader(r.Header.Get("Authorization"))
	if err != nil {
		return nil, errSignatureDoesNotMatch()
	}
	if parsed.Service != sigV4Service || parsed.Terminal != sigV4Terminal {
		return nil, errSignatureDoesNotMatch()
	}
	if !containsSignedHeader(parsed.SignedHeaders, copySourceHeader) {
		return nil, errSignatureDoesNotMatch()
	}
	if !a.acceptAll {
		if _, ok := a.acceptRegions[parsed.Region]; !ok {
			return nil, errSignatureDoesNotMatch()
		}
	}
	secret, ok := a.secrets[parsed.AccessKey]
	if !ok {
		return nil, errInvalidAccessKey()
	}

	requestTime, err := requestSigningTime(r)
	if err != nil {
		return nil, errSignatureDoesNotMatch()
	}
	if requestTime.UTC().Format("20060102") != parsed.Date {
		return nil, errSignatureDoesNotMatch()
	}
	now := a.now()
	if requestTime.Before(now.Add(-a.clockSkew)) || requestTime.After(now.Add(a.clockSkew)) {
		return nil, errRequestTimeTooSkewed()
	}

	payloadHash := strings.TrimSpace(r.Header.Get(payloadHashHeader))
	if payloadHash == "" {
		return nil, errSignatureDoesNotMatch()
	}

	canonicalRequest, err := buildCanonicalRequest(r, parsed.SignedHeaders, payloadHash)
	if err != nil {
		return nil, errSignatureDoesNotMatch()
	}
	stringToSign := buildStringToSign(requestTime, parsed.Region, canonicalRequest)
	expected := calculateSignature(secret, parsed.Date, parsed.Region, stringToSign)
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(expected)), []byte(strings.ToLower(parsed.Signature))) != 1 {
		return nil, errSignatureDoesNotMatch()
	}
	return &AuthResult{AccessKey: parsed.AccessKey, Region: parsed.Region}, nil
}

func containsSignedHeader(headers []string, want string) bool {
	for _, header := range headers {
		if header == want {
			return true
		}
	}
	return false
}

func parseAuthorizationHeader(header string) (*parsedAuthorization, error) {
	header = strings.TrimSpace(header)
	if header == "" {
		return nil, fmt.Errorf("missing authorization")
	}
	algorithm, rest, ok := strings.Cut(header, " ")
	if !ok || algorithm != sigV4Algorithm {
		return nil, fmt.Errorf("unsupported authorization algorithm")
	}
	parts := strings.Split(rest, ",")
	values := make(map[string]string, len(parts))
	for _, part := range parts {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return nil, fmt.Errorf("invalid authorization part")
		}
		values[key] = value
	}
	credential := values["Credential"]
	signedHeaders := values["SignedHeaders"]
	signature := values["Signature"]
	if credential == "" || signedHeaders == "" || signature == "" {
		return nil, fmt.Errorf("authorization is incomplete")
	}
	scope := strings.Split(credential, "/")
	if len(scope) != 5 {
		return nil, fmt.Errorf("credential scope is invalid")
	}
	headers := strings.Split(signedHeaders, ";")
	if len(headers) == 0 {
		return nil, fmt.Errorf("signed headers are empty")
	}
	for i := range headers {
		headers[i] = strings.TrimSpace(strings.ToLower(headers[i]))
		if headers[i] == "" {
			return nil, fmt.Errorf("signed header is empty")
		}
	}
	if !sort.StringsAreSorted(headers) {
		return nil, fmt.Errorf("signed headers are not sorted")
	}
	if len(signature) != 64 || !isLowerHex(signature) {
		return nil, fmt.Errorf("signature is invalid")
	}
	return &parsedAuthorization{
		AccessKey:     scope[0],
		Date:          scope[1],
		Region:        scope[2],
		Service:       scope[3],
		Terminal:      scope[4],
		SignedHeaders: headers,
		Signature:     signature,
	}, nil
}

func requestSigningTime(r *http.Request) (time.Time, error) {
	if value := strings.TrimSpace(r.Header.Get(amzDateHeader)); value != "" {
		return time.Parse("20060102T150405Z", value)
	}
	if value := strings.TrimSpace(r.Header.Get("Date")); value != "" {
		return http.ParseTime(value)
	}
	return time.Time{}, fmt.Errorf("missing request date")
}

func buildCanonicalRequest(r *http.Request, signedHeaders []string, payloadHash string) (string, error) {
	canonicalHeaders, err := canonicalHeaders(r, signedHeaders)
	if err != nil {
		return "", err
	}
	uri := r.URL.EscapedPath()
	if uri == "" {
		uri = "/"
	}
	return strings.Join([]string{
		r.Method,
		uri,
		canonicalQueryString(r.URL.RawQuery),
		canonicalHeaders,
		strings.Join(signedHeaders, ";"),
		payloadHash,
	}, "\n"), nil
}

func canonicalHeaders(r *http.Request, signedHeaders []string) (string, error) {
	var b strings.Builder
	for _, name := range signedHeaders {
		value, ok := signedHeaderValue(r, name)
		if !ok {
			return "", fmt.Errorf("signed header %s is missing", name)
		}
		b.WriteString(name)
		b.WriteByte(':')
		b.WriteString(value)
		b.WriteByte('\n')
	}
	return b.String(), nil
}

func signedHeaderValue(r *http.Request, name string) (string, bool) {
	switch name {
	case "host":
		if r.Host == "" {
			return "", false
		}
		return canonicalHeaderValue(r.Host), true
	case "content-length":
		if r.ContentLength < 0 {
			return "", false
		}
		return fmt.Sprintf("%d", r.ContentLength), true
	default:
		values := r.Header.Values(name)
		if len(values) == 0 {
			return "", false
		}
		normalized := make([]string, 0, len(values))
		for _, value := range values {
			normalized = append(normalized, canonicalHeaderValue(value))
		}
		return strings.Join(normalized, ","), true
	}
}

func canonicalHeaderValue(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func canonicalQueryString(raw string) string {
	if raw == "" {
		return ""
	}
	values, err := url.ParseQuery(raw)
	if err != nil {
		return ""
	}
	type pair struct {
		key   string
		value string
	}
	pairs := make([]pair, 0)
	for key, vals := range values {
		if len(vals) == 0 {
			pairs = append(pairs, pair{key: awsURIEncode(key), value: ""})
			continue
		}
		for _, value := range vals {
			pairs = append(pairs, pair{key: awsURIEncode(key), value: awsURIEncode(value)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].key == pairs[j].key {
			return pairs[i].value < pairs[j].value
		}
		return pairs[i].key < pairs[j].key
	})
	parts := make([]string, 0, len(pairs))
	for _, pair := range pairs {
		parts = append(parts, pair.key+"="+pair.value)
	}
	return strings.Join(parts, "&")
}

func awsURIEncode(value string) string {
	var b strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteString(fmt.Sprintf("%%%02X", c))
	}
	return b.String()
}

func buildStringToSign(requestTime time.Time, region, canonicalRequest string) string {
	scope := strings.Join([]string{requestTime.UTC().Format("20060102"), region, sigV4Service, sigV4Terminal}, "/")
	hash := sha256.Sum256([]byte(canonicalRequest))
	return strings.Join([]string{
		sigV4Algorithm,
		requestTime.UTC().Format("20060102T150405Z"),
		scope,
		hex.EncodeToString(hash[:]),
	}, "\n")
}

func calculateSignature(secretKey, date, region, stringToSign string) string {
	dateKey := hmacSHA256([]byte("AWS4"+secretKey), date)
	regionKey := hmacSHA256(dateKey, region)
	serviceKey := hmacSHA256(regionKey, sigV4Service)
	signingKey := hmacSHA256(serviceKey, sigV4Terminal)
	signature := hmacSHA256(signingKey, stringToSign)
	return hex.EncodeToString(signature)
}

func hmacSHA256(key []byte, data string) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(data))
	return mac.Sum(nil)
}

func isLowerHex(value string) bool {
	for _, c := range value {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func isHexSHA256(value string) bool {
	return len(value) == 64 && isLowerHex(strings.ToLower(value))
}
