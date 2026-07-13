package copygateway

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	addressingAuto    = "auto"
	addressingPath    = "path"
	addressingVirtual = "virtual"
)

type Config struct {
	ListenAddr         string
	PublicS3BaseDomain string
	AddressingStyle    string
	Auth               AuthConfig
	Buckets            BucketMappings
	Source             S3Config
	Target             S3Config
	MaxInFlight        int
	AcquireWait        time.Duration
	SourceCheckTimeout time.Duration
	CopyTimeout        time.Duration
}

type AuthConfig struct {
	AccessKey       string
	SecretKey       string
	CredentialsJSON string
	AcceptRegions   []string
	ClockSkew       time.Duration
}

type S3Config struct {
	Endpoint                   string
	Provider                   string
	Region                     string
	AccessKey                  string
	SecretKey                  string
	PathStyle                  bool
	DialTimeout                time.Duration
	TLSHandshakeTimeout        time.Duration
	ResponseHeaderTimeout      time.Duration
	IdleConnTimeout            time.Duration
	RequestChecksumCalculation string
	ResponseChecksumValidation string
}

func DefaultConfig() Config {
	baseS3 := S3Config{
		DialTimeout:                5 * time.Second,
		TLSHandshakeTimeout:        10 * time.Second,
		ResponseHeaderTimeout:      30 * time.Second,
		IdleConnTimeout:            90 * time.Second,
		RequestChecksumCalculation: "WHEN_REQUIRED",
		ResponseChecksumValidation: "WHEN_REQUIRED",
	}
	source := baseS3
	source.Provider = "s3"
	source.Region = "us-east-1"
	source.PathStyle = true
	target := baseS3
	target.Provider = "s3"
	target.PathStyle = false
	return Config{
		ListenAddr:      ":8080",
		AddressingStyle: addressingAuto,
		Auth: AuthConfig{
			AcceptRegions: []string{"*"},
			ClockSkew:     5 * time.Minute,
		},
		Source:             source,
		Target:             target,
		MaxInFlight:        256,
		AcquireWait:        100 * time.Millisecond,
		SourceCheckTimeout: 2 * time.Second,
		CopyTimeout:        30 * time.Second,
	}
}

func LoadConfig() (Config, error) {
	cfg := DefaultConfig()
	var err error

	cfg.ListenAddr = envString("LISTEN_ADDR", cfg.ListenAddr)
	cfg.PublicS3BaseDomain = envString("PUBLIC_S3_BASE_DOMAIN", cfg.PublicS3BaseDomain)
	cfg.AddressingStyle = strings.ToLower(envString("S3_ADDRESSING_STYLE", cfg.AddressingStyle))

	cfg.Source.Endpoint = envString("SOURCE_S3_ENDPOINT", cfg.Source.Endpoint)
	cfg.Source.Region = envString("SOURCE_S3_REGION", cfg.Source.Region)
	cfg.Source.AccessKey = envString("SOURCE_S3_ACCESS_KEY", cfg.Source.AccessKey)
	cfg.Source.SecretKey = envString("SOURCE_S3_SECRET_KEY", cfg.Source.SecretKey)
	if cfg.Source.PathStyle, err = envBool("SOURCE_S3_PATH_STYLE", cfg.Source.PathStyle); err != nil {
		return Config{}, err
	}
	if err := loadS3Env("SOURCE", &cfg.Source); err != nil {
		return Config{}, err
	}

	cfg.Target.Provider = strings.ToLower(envString("TARGET_S3_PROVIDER", cfg.Target.Provider))
	cfg.Target.Endpoint = envString("TARGET_S3_ENDPOINT", cfg.Target.Endpoint)
	cfg.Target.Region = envString("TARGET_S3_REGION", cfg.Target.Region)
	cfg.Target.AccessKey = envString("TARGET_S3_ACCESS_KEY", cfg.Target.AccessKey)
	cfg.Target.SecretKey = envString("TARGET_S3_SECRET_KEY", cfg.Target.SecretKey)
	if cfg.Target.PathStyle, err = envBool("TARGET_S3_PATH_STYLE", cfg.Target.PathStyle); err != nil {
		return Config{}, err
	}
	if err := loadS3Env("TARGET", &cfg.Target); err != nil {
		return Config{}, err
	}

	cfg.Auth.AccessKey = envString("S3_ACCESS_KEY", cfg.Source.AccessKey)
	cfg.Auth.SecretKey = envString("S3_SECRET_KEY", cfg.Source.SecretKey)
	cfg.Auth.CredentialsJSON = envString("S3_CREDENTIALS_JSON", "")
	cfg.Auth.AcceptRegions = envCSV("AUTH_ACCEPT_REGIONS", cfg.Auth.AcceptRegions)
	if cfg.Auth.ClockSkew, err = envDuration("AUTH_CLOCK_SKEW", cfg.Auth.ClockSkew); err != nil {
		return Config{}, err
	}
	if cfg.MaxInFlight, err = envInt("MAX_IN_FLIGHT", cfg.MaxInFlight); err != nil {
		return Config{}, err
	}
	if cfg.AcquireWait, err = envDuration("ACQUIRE_WAIT", cfg.AcquireWait); err != nil {
		return Config{}, err
	}
	if cfg.SourceCheckTimeout, err = envDuration("SOURCE_CHECK_TIMEOUT", cfg.SourceCheckTimeout); err != nil {
		return Config{}, err
	}
	if cfg.CopyTimeout, err = envDuration("COPY_TIMEOUT", cfg.CopyTimeout); err != nil {
		return Config{}, err
	}
	cfg.Buckets, err = ParseBucketMappings(envString("BUCKET_MAPPINGS_JSON", ""))
	if err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func loadS3Env(prefix string, cfg *S3Config) error {
	var err error
	if cfg.DialTimeout, err = envDuration(prefix+"_DIAL_TIMEOUT", cfg.DialTimeout); err != nil {
		return err
	}
	if cfg.TLSHandshakeTimeout, err = envDuration(prefix+"_TLS_HANDSHAKE_TIMEOUT", cfg.TLSHandshakeTimeout); err != nil {
		return err
	}
	if cfg.ResponseHeaderTimeout, err = envDuration(prefix+"_RESPONSE_HEADER_TIMEOUT", cfg.ResponseHeaderTimeout); err != nil {
		return err
	}
	if cfg.IdleConnTimeout, err = envDuration(prefix+"_IDLE_CONN_TIMEOUT", cfg.IdleConnTimeout); err != nil {
		return err
	}
	cfg.RequestChecksumCalculation = envString(prefix+"_REQUEST_CHECKSUM_CALCULATION", cfg.RequestChecksumCalculation)
	cfg.ResponseChecksumValidation = envString(prefix+"_RESPONSE_CHECKSUM_VALIDATION", cfg.ResponseChecksumValidation)
	return nil
}

func (c Config) Validate() error {
	if strings.TrimSpace(c.ListenAddr) == "" {
		return fmt.Errorf("LISTEN_ADDR is empty")
	}
	switch c.AddressingStyle {
	case addressingAuto, addressingPath, addressingVirtual:
	default:
		return fmt.Errorf("unsupported S3_ADDRESSING_STYLE: %s", c.AddressingStyle)
	}
	if c.AddressingStyle == addressingVirtual && strings.TrimSpace(c.PublicS3BaseDomain) == "" {
		return fmt.Errorf("PUBLIC_S3_BASE_DOMAIN is required for virtual-host addressing")
	}
	credentials, err := c.Auth.CredentialsStrict()
	if err != nil {
		return err
	}
	if len(credentials) == 0 {
		return fmt.Errorf("missing S3 access key config")
	}
	if err := validateS3Config("source", c.Source, true); err != nil {
		return err
	}
	if err := validateS3Config("target", c.Target, false); err != nil {
		return err
	}
	if !c.Buckets.identity && len(c.Buckets.targets) == 0 {
		return fmt.Errorf("BUCKET_MAPPINGS_JSON is required")
	}
	provider := strings.ToLower(strings.TrimSpace(c.Target.Provider))
	if provider == "ovh" || (strings.TrimSpace(c.Target.Endpoint) == "" && strings.Contains(providerEndpointTemplates[provider], "%s")) {
		if err := c.Buckets.ValidateRegions(c.Target.Region); err != nil {
			return err
		}
	}
	if c.MaxInFlight <= 0 {
		return fmt.Errorf("MAX_IN_FLIGHT must be positive")
	}
	if c.AcquireWait < 0 {
		return fmt.Errorf("ACQUIRE_WAIT must not be negative")
	}
	if c.SourceCheckTimeout <= 0 {
		return fmt.Errorf("SOURCE_CHECK_TIMEOUT must be positive")
	}
	if c.CopyTimeout <= 0 {
		return fmt.Errorf("COPY_TIMEOUT must be positive")
	}
	if c.SourceCheckTimeout > c.CopyTimeout {
		return fmt.Errorf("SOURCE_CHECK_TIMEOUT must not exceed COPY_TIMEOUT")
	}
	return nil
}

func validateS3Config(name string, cfg S3Config, requireEndpoint bool) error {
	if strings.TrimSpace(cfg.AccessKey) == "" || strings.TrimSpace(cfg.SecretKey) == "" {
		return fmt.Errorf("missing %s S3 credentials", name)
	}
	if requireEndpoint && strings.TrimSpace(cfg.Endpoint) == "" {
		return fmt.Errorf("missing %s S3 endpoint", name)
	}
	if !requireEndpoint && strings.TrimSpace(cfg.Endpoint) == "" && providerEndpointTemplates[strings.ToLower(strings.TrimSpace(cfg.Provider))] == "" {
		return fmt.Errorf("missing target S3 endpoint or supported provider")
	}
	for key, value := range map[string]time.Duration{
		"dial timeout":            cfg.DialTimeout,
		"TLS handshake timeout":   cfg.TLSHandshakeTimeout,
		"response header timeout": cfg.ResponseHeaderTimeout,
		"idle connection timeout": cfg.IdleConnTimeout,
	} {
		if value <= 0 {
			return fmt.Errorf("%s S3 %s must be positive", name, key)
		}
	}
	if err := validateAWSChecksumOption(name+" request checksum calculation", cfg.RequestChecksumCalculation); err != nil {
		return err
	}
	return validateAWSChecksumOption(name+" response checksum validation", cfg.ResponseChecksumValidation)
}

func (c AuthConfig) CredentialsStrict() ([]Credential, error) {
	credentials := make([]Credential, 0, 1)
	if strings.TrimSpace(c.AccessKey) != "" && strings.TrimSpace(c.SecretKey) != "" {
		credentials = append(credentials, Credential{AccessKey: strings.TrimSpace(c.AccessKey), SecretKey: strings.TrimSpace(c.SecretKey)})
	}
	if strings.TrimSpace(c.CredentialsJSON) == "" {
		return credentials, nil
	}
	extra, err := parseCredentialsJSON(c.CredentialsJSON)
	if err != nil {
		return nil, fmt.Errorf("S3_CREDENTIALS_JSON is invalid: %w", err)
	}
	return append(credentials, extra...), nil
}

func parseCredentialsJSON(raw string) ([]Credential, error) {
	var list []Credential
	if err := json.Unmarshal([]byte(raw), &list); err == nil {
		return compactCredentials(list), nil
	}
	var wrapped struct {
		Credentials []Credential `json:"credentials"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapped); err == nil && wrapped.Credentials != nil {
		return compactCredentials(wrapped.Credentials), nil
	}
	values := make(map[string]string)
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	for accessKey, secretKey := range values {
		list = append(list, Credential{AccessKey: accessKey, SecretKey: secretKey})
	}
	return compactCredentials(list), nil
}

func compactCredentials(in []Credential) []Credential {
	out := make([]Credential, 0, len(in))
	for _, credential := range in {
		credential.AccessKey = strings.TrimSpace(credential.AccessKey)
		credential.SecretKey = strings.TrimSpace(credential.SecretKey)
		if credential.AccessKey != "" && credential.SecretKey != "" {
			out = append(out, credential)
		}
	}
	return out
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envCSV(name string, fallback []string) []string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return cleanStrings(strings.Split(value, ","))
	}
	return fallback
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s has invalid duration %q: %w", name, value, err)
	}
	return parsed, nil
}

func envInt(name string, fallback int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s has invalid integer %q: %w", name, value, err)
	}
	return parsed, nil
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s has invalid boolean %q: %w", name, value, err)
	}
	return parsed, nil
}

func validateAWSChecksumOption(name, value string) error {
	switch strings.ToUpper(strings.TrimSpace(value)) {
	case "", "WHEN_REQUIRED", "WHEN_SUPPORTED":
		return nil
	default:
		return fmt.Errorf("%s has unsupported value: %s", name, value)
	}
}

func cleanStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}
