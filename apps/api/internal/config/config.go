// Package config loads the API configuration from environment variables.
//
// M0 foundation only. It exposes a minimal, dependency-free configuration
// surface consumed by cmd/api; it performs no I/O beyond reading the process
// environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds the runtime configuration for the API process.
type Config struct {
	// Addr is the host:port the HTTP server listens on.
	Addr string
	// ShutdownTimeout bounds graceful shutdown of in-flight requests.
	ShutdownTimeout time.Duration
	// ReadinessTimeout bounds a single readiness probe evaluation.
	ReadinessTimeout time.Duration
	// LogLevel selects the structured logging verbosity.
	LogLevel string
	// Dependencies names the optional dependency probes wired into readiness.
	Dependencies []string
	// LocalAdminEnabled is explicitly enabled only for development.
	LocalAdminEnabled bool
	// Environment controls whether local administration may be enabled.
	Environment string
	// LocalAdminToken is a secret and must never be logged.
	LocalAdminToken string
	// DatabaseURL is required only when local administration is enabled. It may
	// carry credentials, so it must never be logged.
	DatabaseURL string
	// RedisURL is the connection URL of the coordination layer that holds the
	// per-seat leases. It defaults to the local compose service.
	RedisURL string
	// HoldTTL is the reservation hold window. It defaults to ten minutes and
	// accepts any Go duration so tests can use seconds, but it must be positive.
	HoldTTL time.Duration
	// ExpiryInterval is the cadence of the durable expiry sweep. It defaults to
	// 30s, lowered to half the hold window when that window is shorter than a
	// minute, and it can never exceed half the hold window: a sweep has to fire at
	// least twice before a hold it should expire would be re-claimed.
	ExpiryInterval time.Duration
	// ExpiryBatch is how many expired holds one sweep transaction locks at most.
	// It defaults to 100 and must be positive.
	ExpiryBatch int
	// S3Endpoint is the base URL of the object-storage service that holds the
	// ticket PDFs. It defaults to the local Floci compose service.
	S3Endpoint string
	// S3Bucket is the bucket the API may write the ticket PDFs to. It defaults to
	// a cinema-m3-* name, the namespace local development may create.
	S3Bucket string
	// S3Region is the signing region. S3 needs one even for a local emulator, so
	// it defaults to us-east-1.
	S3Region string
	// S3AccessKeyID identifies the object-storage credential. It defaults to the
	// dummy value Floci accepts, which is refused for a non-local endpoint.
	S3AccessKeyID string
	// S3SecretAccessKey is a secret and must never be logged or echoed in an
	// error. It defaults to the dummy value Floci accepts.
	S3SecretAccessKey string
	// S3PresignTTL is how long a ticket PDF download link stays valid. It defaults
	// to five minutes and can never exceed the seven day signature limit.
	S3PresignTTL time.Duration
	// TicketQRKey is the HMAC key the ticket QR tokens are signed with. It is a
	// secret and must never be logged, echoed in an error or committed: a key that
	// ships in a repository signs nothing, which is why the development default is
	// accepted only when Environment is development.
	TicketQRKey string
	// TicketLinkKey is the HMAC key the signed ticket links a purchase email carries
	// are signed with. It is a secret and must never be logged, echoed in an error or
	// committed, and it is deliberately not the QR key: rotating one must not
	// invalidate the other, so neither can be derived from the other.
	TicketLinkKey string
	// TicketLinkTTL is how long a signed ticket link keeps opening the order's
	// tickets. It defaults to the documented seven day demo window and must be
	// positive: a non-positive lifetime would mint links that are already closed.
	TicketLinkTTL time.Duration
}

// Documented defaults for the reservation surface.
const (
	// defaultHoldTTL is the ten minute window a hold lasts unless configured.
	defaultHoldTTL = 10 * time.Minute
	// defaultRedisURL is the local compose coordination service.
	defaultRedisURL = "redis://127.0.0.1:6379/0"
	// defaultExpiryInterval is the sweep cadence; a shorter hold window lowers it
	// to half the window so the sweep always fires twice per hold.
	defaultExpiryInterval = 30 * time.Second
	// defaultExpiryBatch bounds one sweep transaction to a hundred holds.
	defaultExpiryBatch = 100
	// maxExpiryBatch keeps a misconfigured batch from locking an unbounded row set
	// in one transaction.
	maxExpiryBatch = 1000
)

// Documented defaults for the object-storage surface. They describe the local
// Floci compose service, so a developer needs no S3 variables at all; production
// supplies its own endpoint, bucket and credentials.
const (
	// defaultS3Endpoint is the Floci compose service. It speaks the S3 API
	// path-style on port 4566.
	defaultS3Endpoint = "http://127.0.0.1:4566"
	// defaultS3Bucket is inside the cinema-m3-* namespace this project owns, so a
	// verification run can never touch a bucket that already held data.
	defaultS3Bucket = "cinema-m3-tickets"
	// defaultS3Region is required for signing even when the emulator ignores it.
	defaultS3Region = "us-east-1"
	// defaultS3AccessKeyID and defaultS3SecretAccessKey are the dummy pair Floci
	// accepts. They are development values only and are refused for a non-local
	// endpoint.
	defaultS3AccessKeyID     = "floci"
	defaultS3SecretAccessKey = "floci"
	// defaultS3PresignTTL is the short-lived window a ticket download link lives
	// for, matching the M3 decision that PDFs are served by pre-signed URL.
	defaultS3PresignTTL = 5 * time.Minute
	// maxS3PresignTTL is the longest lifetime a SigV4 pre-signed URL supports.
	maxS3PresignTTL = 7 * 24 * time.Hour
)

// The ticket QR signing key rules. The code printed on the web page and on the PDF
// is an HMAC-SHA256 signature over the ticket's identity, so the key behind it is
// the whole of its security: a key anybody can read signs nothing at all.
const (
	// developmentTicketQRKey is the well-known development key, accepted only when
	// API_ENV is development. It exists so a developer can run the whole checkout with
	// no variables at all, and it is refused everywhere else: an environment that
	// trusts it would sign tickets with a key that is published in this repository.
	developmentTicketQRKey = "cinema-m3-development-ticket-qr-key"
	// ticketQRKeyMinLength is the shortest key accepted, in bytes. It matches the
	// length of a SHA-256 digest, so the number a deployment has to satisfy is the one
	// its hash function already implies.
	ticketQRKeyMinLength = 32
)

// The ticket link rules. A purchase email carries a signed link instead of the ticket's
// permanent URL, so the link needs a key of its own and a lifetime: a QR code is printed
// on paper and a link lives in a mailbox, and neither may be able to invalidate the other.
const (
	// developmentTicketLinkKey is the well-known development link key, accepted only when
	// API_ENV is development. It is deliberately a different value from
	// developmentTicketQRKey, so a deployment that mixes the two up still fails instead of
	// silently signing both with one key.
	developmentTicketLinkKey = "cinema-m3-development-ticket-link-key"
	// ticketLinkKeyMinLength is the shortest link key accepted, in bytes, for the reason
	// ticketQRKeyMinLength states: an HMAC-SHA256 key is only as strong as its entropy.
	ticketLinkKeyMinLength = 32
	// defaultTicketLinkTTL is the seven day window a purchase email's link lives. It is the
	// demo value and it is documented as such: long enough to survive a booking made on a
	// Friday for a screening the next weekend, short enough that an email read a month
	// later opens nothing and the buyer comes back through the page instead.
	defaultTicketLinkTTL = 7 * 24 * time.Hour
)

// Load reads configuration from the process environment.
func Load() (Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom reads configuration using getenv, enabling deterministic tests.
//
// Recognized variables:
//
//	API_ADDR                   listen address (default ":8080")
//	API_LOG_LEVEL              log level debug|info|warn|error (default "info")
//	API_SHUTDOWN_TIMEOUT_MS    graceful shutdown budget in milliseconds (default 15000)
//	API_READINESS_TIMEOUT_MS   readiness deadline in milliseconds (default 5000)
//	API_DEPENDENCIES           comma-separated dependency probe names (default none)
//	API_ENV                    environment; local administration requires development
//	API_LOCAL_ADMIN            true or false; defaults to false
//	API_LOCAL_ADMIN_TOKEN      secret required when local administration is enabled
//	API_DATABASE_URL           database URL; required when local administration is enabled
//	API_HOLD_TTL              reservation hold window as a duration (default "10m"; "30s" is valid)
//	API_REDIS_URL             coordination URL for the seat leases (default "redis://127.0.0.1:6379/0")
//	API_EXPIRY_INTERVAL       durable expiry sweep cadence as a duration (default "30s", at most half of API_HOLD_TTL)
//	API_EXPIRY_BATCH          expired holds one sweep locks at most (default 100, at most 1000)
//	API_S3_ENDPOINT           object-storage base URL, absolute with no path (default "http://127.0.0.1:4566", the Floci service)
//	API_S3_BUCKET             bucket holding the ticket PDFs (default "cinema-m3-tickets")
//	API_S3_REGION             signing region (default "us-east-1")
//	API_S3_ACCESS_KEY_ID      credential identifier (default "floci", refused for a non-local endpoint)
//	API_S3_SECRET_ACCESS_KEY  secret credential, always set together with the key id and never echoed
//	API_S3_PRESIGN_TTL        download link lifetime as a duration (default "5m", at most "168h")
//	API_TICKET_QR_KEY         HMAC key the ticket QR tokens are signed with: at least 32
//	                          bytes, a secret, never echoed (default: the development key,
//	                          and only when API_ENV=development)
//	API_TICKET_LINK_KEY       HMAC key the signed ticket links in purchase emails are
//	                          signed with: at least 32 bytes, a secret, never echoed, and
//	                          never the QR key (default: the development key, and only
//	                          when API_ENV=development)
//	API_TICKET_LINK_TTL       lifetime of a signed ticket link as a duration (default
//	                          "168h", the documented seven day demo window; positive)
func LoadFrom(getenv func(string) string) (Config, error) {
	cfg := Config{
		Addr:             ":8080",
		ShutdownTimeout:  15 * time.Second,
		ReadinessTimeout: 5 * time.Second,
		LogLevel:         "info",
		Dependencies:     []string{},
		RedisURL:         defaultRedisURL,
		HoldTTL:          defaultHoldTTL,
		ExpiryBatch:      defaultExpiryBatch,
	}

	cfg.Environment = getenv("API_ENV")
	cfg.LocalAdminToken = getenv("API_LOCAL_ADMIN_TOKEN")
	cfg.DatabaseURL = strings.TrimSpace(getenv("API_DATABASE_URL"))
	if v := getenv("API_LOCAL_ADMIN"); v != "" {
		if v != "true" && v != "false" {
			return Config{}, fmt.Errorf("API_LOCAL_ADMIN must be true or false")
		}
		cfg.LocalAdminEnabled = v == "true"
	}
	if cfg.LocalAdminEnabled && (cfg.Environment != "development" || cfg.LocalAdminToken == "" || cfg.DatabaseURL == "") {
		// The message names the required settings but never echoes their values,
		// because the token and the database URL may both carry secrets.
		return Config{}, fmt.Errorf("local administration requires development, a nonempty token, and a database URL")
	}

	if v := getenv("API_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := getenv("API_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}

	if v := getenv("API_SHUTDOWN_TIMEOUT_MS"); v != "" {
		d, err := parseMillis(v, "API_SHUTDOWN_TIMEOUT_MS")
		if err != nil {
			return Config{}, err
		}
		cfg.ShutdownTimeout = d
	}
	if v := getenv("API_READINESS_TIMEOUT_MS"); v != "" {
		d, err := parseMillis(v, "API_READINESS_TIMEOUT_MS")
		if err != nil {
			return Config{}, err
		}
		cfg.ReadinessTimeout = d
	}

	if v := getenv("API_DEPENDENCIES"); v != "" {
		cfg.Dependencies = parseDependencies(v)
	}

	if v := getenv("API_REDIS_URL"); v != "" {
		cfg.RedisURL = strings.TrimSpace(v)
	}

	if v := getenv("API_HOLD_TTL"); v != "" {
		d, err := parseHoldTTL(v)
		if err != nil {
			return Config{}, err
		}
		cfg.HoldTTL = d
	}

	// The sweep cadence defaults to the shorter of the documented 30s and half the
	// hold window, so a short window still gets two sweeps per hold.
	cfg.ExpiryInterval = defaultExpiryInterval
	if half := cfg.HoldTTL / 2; cfg.ExpiryInterval > half {
		cfg.ExpiryInterval = half
	}
	if v := getenv("API_EXPIRY_INTERVAL"); v != "" {
		d, err := parseExpiryInterval(v)
		if err != nil {
			return Config{}, err
		}
		cfg.ExpiryInterval = d
	}
	// A cadence slower than half the hold window is refused rather than clamped: a
	// silently shortened interval would hide a misconfiguration.
	if half := cfg.HoldTTL / 2; cfg.ExpiryInterval <= 0 || cfg.ExpiryInterval > half {
		return Config{}, fmt.Errorf("API_EXPIRY_INTERVAL must be positive and at most half of API_HOLD_TTL (%s), got %s",
			half, cfg.ExpiryInterval)
	}

	if v := getenv("API_EXPIRY_BATCH"); v != "" {
		batch, err := parseExpiryBatch(v)
		if err != nil {
			return Config{}, err
		}
		cfg.ExpiryBatch = batch
	}

	if err := cfg.loadObjectStorage(getenv); err != nil {
		return Config{}, err
	}

	if err := cfg.loadTicketQR(getenv); err != nil {
		return Config{}, err
	}

	if err := cfg.loadTicketLink(getenv); err != nil {
		return Config{}, err
	}

	return cfg, nil
}

// loadTicketQR reads and validates API_TICKET_QR_KEY, the key the ticket QR tokens
// are signed with.
//
// Two things are refused here, both at startup and both without echoing the key:
//
//   - a key that cannot sign anything, because it is blank or shorter than
//     ticketQRKeyMinLength bytes;
//   - the well-known development default in any environment but development, because
//     a key that ships in a repository signs nothing anybody should trust. That covers
//     the accidental copy as well as the missing key: an operator who pastes the
//     development value into production is refused rather than started.
//
// An absent key is not refused here, and the reason is precise: this configuration is
// shared by commands that never serve a ticket (`bootstrap wait-deps` and
// `bootstrap ensure-bucket` load it too), so refusing here would break them for a
// requirement they do not have. The requirement is enforced where it is real — the
// API process refuses to mount the ticket surface without a usable key (see cmd/api),
// so a deployment that can serve a ticket cannot start without one, and a deployment
// that serves none never needs it. In development the documented default is applied
// so a local run needs no variables at all.
func (c *Config) loadTicketQR(getenv func(string) string) error {
	key := getenv("API_TICKET_QR_KEY")
	if key == "" {
		if c.Environment == "development" {
			c.TicketQRKey = developmentTicketQRKey
		}
		return nil
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("API_TICKET_QR_KEY must not be blank: a blank key signs nothing")
	}
	if len(key) < ticketQRKeyMinLength {
		return fmt.Errorf("API_TICKET_QR_KEY must be at least %d bytes of entropy-bearing text, got %d",
			ticketQRKeyMinLength, len(key))
	}
	if key == developmentTicketQRKey && c.Environment != "development" {
		return fmt.Errorf("API_TICKET_QR_KEY still holds the development default, which only API_ENV=development accepts: a key that ships in a repository signs nothing")
	}
	c.TicketQRKey = key
	return nil
}

// loadTicketLink reads and validates API_TICKET_LINK_KEY and API_TICKET_LINK_TTL, the key
// and lifetime of the signed link a purchase email carries.
//
// The key rules are the QR key's, applied to a key of its own: blank and too short are
// refused at startup, the well-known development default is refused outside development,
// and an absent key is left to the process that serves the links, for the reason
// [Config.loadTicketQR] states — this configuration is shared by commands that never sign
// anything. The lifetime has a documented default and refuses a value that would mint
// links which are already closed.
func (c *Config) loadTicketLink(getenv func(string) string) error {
	key := getenv("API_TICKET_LINK_KEY")
	switch {
	case key == "":
		if c.Environment == "development" {
			c.TicketLinkKey = developmentTicketLinkKey
		}
	case strings.TrimSpace(key) == "":
		return fmt.Errorf("API_TICKET_LINK_KEY must not be blank: a blank key signs nothing")
	case len(key) < ticketLinkKeyMinLength:
		return fmt.Errorf("API_TICKET_LINK_KEY must be at least %d bytes of entropy-bearing text, got %d",
			ticketLinkKeyMinLength, len(key))
	case key == developmentTicketLinkKey && c.Environment != "development":
		return fmt.Errorf("API_TICKET_LINK_KEY still holds the development default, which only API_ENV=development accepts: a key that ships in a repository signs nothing")
	default:
		c.TicketLinkKey = key
	}

	c.TicketLinkTTL = defaultTicketLinkTTL
	if v := strings.TrimSpace(getenv("API_TICKET_LINK_TTL")); v != "" {
		ttl, err := parseTicketLinkTTL(v)
		if err != nil {
			return err
		}
		c.TicketLinkTTL = ttl
	}
	return nil
}

// loadObjectStorage reads and validates the API_S3_* settings. The defaults point
// at the local Floci compose service with the dummy credentials it accepts, so
// local development works with no S3 variables, and every combination that cannot
// work is refused at startup instead of failing during checkout.
func (c *Config) loadObjectStorage(getenv func(string) string) error {
	c.S3Endpoint = defaultS3Endpoint
	if v := strings.TrimSpace(getenv("API_S3_ENDPOINT")); v != "" {
		endpoint, err := normalizeS3Endpoint(v)
		if err != nil {
			return err
		}
		c.S3Endpoint = endpoint
	}

	c.S3Bucket = defaultS3Bucket
	if v := strings.TrimSpace(getenv("API_S3_BUCKET")); v != "" {
		if !validBucketName(v) {
			// The bucket name is not a secret, so naming it helps the operator.
			return fmt.Errorf("API_S3_BUCKET must be a valid S3 bucket name of 3 to 63 lower case letters, digits, dots or hyphens, starting and ending with a letter or a digit, got %q", v)
		}
		c.S3Bucket = v
	}

	c.S3Region = defaultS3Region
	if v := getenv("API_S3_REGION"); v != "" {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			return fmt.Errorf("API_S3_REGION must not be blank")
		}
		c.S3Region = trimmed
	}

	// A half-configured credential pair cannot sign anything, so it is refused
	// instead of being completed with the development default. Neither value is
	// ever echoed: the secret access key may be a real one.
	accessKeyID := strings.TrimSpace(getenv("API_S3_ACCESS_KEY_ID"))
	secretAccessKey := strings.TrimSpace(getenv("API_S3_SECRET_ACCESS_KEY"))
	switch {
	case accessKeyID != "" && secretAccessKey != "":
		c.S3AccessKeyID = accessKeyID
		c.S3SecretAccessKey = secretAccessKey
	case accessKeyID == "" && secretAccessKey == "":
		c.S3AccessKeyID = defaultS3AccessKeyID
		c.S3SecretAccessKey = defaultS3SecretAccessKey
	default:
		return fmt.Errorf("API_S3_ACCESS_KEY_ID and API_S3_SECRET_ACCESS_KEY must be set together")
	}

	// The development credentials cannot work anywhere but on this machine, so
	// pairing them with a remote endpoint is refused rather than attempted.
	if c.S3AccessKeyID == defaultS3AccessKeyID && c.S3SecretAccessKey == defaultS3SecretAccessKey && !isLocalEndpoint(c.S3Endpoint) {
		return fmt.Errorf("API_S3_ACCESS_KEY_ID and API_S3_SECRET_ACCESS_KEY still hold the Floci development defaults, which only a local endpoint accepts")
	}

	c.S3PresignTTL = defaultS3PresignTTL
	if v := strings.TrimSpace(getenv("API_S3_PRESIGN_TTL")); v != "" {
		ttl, err := parsePresignTTL(v)
		if err != nil {
			return err
		}
		c.S3PresignTTL = ttl
	}

	return nil
}

// normalizeS3Endpoint validates the object-storage base URL and drops a trailing
// slash. Path-style addressing appends "/<bucket>/<key>" to the endpoint, so a
// path in it would silently become part of every object path.
func normalizeS3Endpoint(v string) (string, error) {
	u, err := url.Parse(v)
	if err != nil {
		// The raw value may embed credentials, so it is never echoed here.
		return "", fmt.Errorf("API_S3_ENDPOINT must be an absolute http or https URL such as http://127.0.0.1:4566")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("API_S3_ENDPOINT must be an absolute http or https URL such as http://127.0.0.1:4566, got scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("API_S3_ENDPOINT must include a host, such as http://127.0.0.1:4566")
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("API_S3_ENDPOINT must not carry a path, a query, a fragment or user information")
	}
	return strings.TrimRight(v, "/"), nil
}

// isLocalEndpoint reports whether the endpoint points at this machine, which is
// the only place the dummy Floci credentials can work.
func isLocalEndpoint(endpoint string) bool {
	u, err := url.Parse(endpoint)
	if err != nil {
		return false
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// parsePresignTTL accepts a Go duration such as "5m" and refuses a lifetime that
// a SigV4 signature cannot carry, which would produce a link that never works.
func parsePresignTTL(v string) (time.Duration, error) {
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("API_S3_PRESIGN_TTL must be a duration such as 5m or 90s: %w", err)
	}
	if d < time.Second {
		// A SigV4 signature carries whole seconds: a shorter lifetime would be
		// signed as X-Amz-Expires=0 and produce a link that never works.
		return 0, fmt.Errorf("API_S3_PRESIGN_TTL must be at least 1s, the smallest lifetime SigV4 can carry, got %q", v)
	}
	if d > maxS3PresignTTL {
		return 0, fmt.Errorf("API_S3_PRESIGN_TTL must be at most %s, the longest SigV4 signature lifetime, got %q", maxS3PresignTTL, v)
	}
	return d, nil
}

// validBucketName applies the S3 bucket naming rules: lower case letters, digits,
// dots and hyphens, three to sixty-three characters, starting and ending with a
// letter or a digit, and never an address-shaped name. The storage package
// validates its own configuration with the same rules, so a bucket that reaches
// one is never refused by the other.
func validBucketName(name string) bool {
	if len(name) < 3 || len(name) > 63 {
		return false
	}
	if strings.Contains(name, "..") || strings.Contains(name, ".-") || strings.Contains(name, "-.") {
		return false
	}
	addressLike := true
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z':
			addressLike = false
		case c >= '0' && c <= '9':
		case c == '-' || c == '.':
			if i == 0 || i == len(name)-1 {
				return false
			}
		default:
			return false
		}
	}
	// A name without a letter is an address such as 192.168.0.1, which S3
	// refuses because it would collide with a host name.
	return !addressLike
}

// parseTicketLinkTTL accepts a Go duration such as "168h" or, for tests, "90m". The
// lifetime has no other bound: how long an email stays useful is a product decision, and
// the one rule the signer cannot work without is that some window exists at all.
func parseTicketLinkTTL(v string) (time.Duration, error) {
	ttl, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("API_TICKET_LINK_TTL must be a duration such as 168h or 90m: %w", err)
	}
	if ttl <= 0 {
		return 0, fmt.Errorf("API_TICKET_LINK_TTL must be positive, got %q", v)
	}
	return ttl, nil
}

// parseExpiryInterval accepts a Go duration such as "30s" or "5m" for the sweep
// cadence. A value without a unit or a non-positive value is rejected instead of
// spinning a worker loop.
func parseExpiryInterval(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("API_EXPIRY_INTERVAL must be a duration such as 30s or 5m: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("API_EXPIRY_INTERVAL must be positive, got %q", v)
	}
	return d, nil
}

// parseExpiryBatch bounds how many expired holds one sweep transaction locks. A
// non-positive batch could never make progress and an oversized one would lock an
// unbounded row set, so both are refused.
func parseExpiryBatch(v string) (int, error) {
	batch, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("API_EXPIRY_BATCH must be a whole number: %w", err)
	}
	if batch <= 0 {
		return 0, fmt.Errorf("API_EXPIRY_BATCH must be positive, got %q", v)
	}
	if batch > maxExpiryBatch {
		return 0, fmt.Errorf("API_EXPIRY_BATCH must be at most %d, got %q", maxExpiryBatch, v)
	}
	return batch, nil
}

// parseHoldTTL accepts a Go duration such as "10m" or, for tests, "30s".
// A value without a unit or a non-positive window is rejected rather than
// silently treated as a zero-length hold.
func parseHoldTTL(v string) (time.Duration, error) {
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		return 0, fmt.Errorf("API_HOLD_TTL must be a duration such as 10m or 30s: %w", err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("API_HOLD_TTL must be positive, got %q", v)
	}
	return d, nil
}

func parseMillis(v, key string) (time.Duration, error) {
	ms, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if ms < 0 {
		return 0, fmt.Errorf("%s: %d is negative", key, ms)
	}
	return time.Duration(ms) * time.Millisecond, nil
}

func parseDependencies(v string) []string {
	deps := []string{}
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			deps = append(deps, p)
		}
	}
	return deps
}
