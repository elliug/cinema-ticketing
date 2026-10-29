// Package storage is the object-storage boundary for the M3 ticket PDFs.
//
// The PDF is written once, when a payment is confirmed, and read back only
// through a short-lived pre-signed URL, so the stored bytes are the immutable
// artefact that matches what was sold. The boundary is one small interface,
// Store, with Put, Get and PresignGet: a real object store replaces only the
// adapter behind it, exactly as a real payment provider replaces the payment
// adapter (design S8).
//
// # Scope
//
// The package has no HTTP and no database dependency: it knows bytes, keys and
// lifetimes, and what an unavailable store means for a request (fail with 503,
// never serve a missing ticket) is decided by the caller. S3 is implemented by
// the S3 adapter, which addresses the endpoint path-style so a local Floci
// container behaves like the hosted service.
//
// # Fail closed
//
// NewS3 validates its configuration and refuses a combination that cannot work,
// and every method re-checks its receiver. A zero-value or unconfigured adapter
// therefore returns ErrNotConfigured instead of pretending to store or to read an
// object, so a caller can never report a stored PDF that was never written.
//
// # Keys
//
// Keys are validated by ValidateKey before anything is signed or sent. A key is a
// bucket-relative object name, never an absolute path and never a traversal, so a
// signed URL can only ever address an object inside the configured bucket.
package storage

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// Failures reported by this package. Callers classify with errors.Is and decide
// the HTTP outcome themselves.
var (
	// ErrNotConfigured reports that the adapter was never configured, or that a
	// required setting is missing. It is a fail-closed answer, never a success.
	ErrNotConfigured = errors.New("storage: object storage is not configured")

	// ErrInvalidConfig reports a setting that is present but unusable, such as an
	// endpoint with no scheme or a bucket name S3 would refuse.
	ErrInvalidConfig = errors.New("storage: invalid object storage configuration")

	// ErrInvalidKey reports an object key that is empty, absolute, a prefix, or
	// otherwise unsafe to sign.
	ErrInvalidKey = errors.New("storage: invalid object key")

	// ErrInvalidContentType reports a missing or malformed media type.
	ErrInvalidContentType = errors.New("storage: invalid content type")

	// ErrInvalidTTL reports a pre-sign lifetime that is not positive or that
	// exceeds the seven days SigV4 signatures are limited to.
	ErrInvalidTTL = errors.New("storage: invalid pre-sign lifetime")
)

const (
	// maxKeyLength bounds an object key. S3 accepts longer keys, but a ticket PDF
	// key is short by construction and a bounded key keeps URLs and signatures
	// bounded too.
	maxKeyLength = 1024

	// maxPresignTTL is the longest lifetime a SigV4 pre-signed URL supports. A
	// longer request is refused instead of silently clamped, so a caller that
	// expects a working link learns about it at signing time.
	maxPresignTTL = 7 * 24 * time.Hour
)

// Store is the replaceable object-storage boundary.
type Store interface {
	// Put stores data under key with contentType, replacing any existing object.
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Get returns the stored bytes for key.
	Get(ctx context.Context, key string) ([]byte, error)
	// PresignGet returns a URL that downloads key without credentials and stops
	// working after ttl.
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Config is the adapter configuration. It mirrors the API_S3_* settings.
type Config struct {
	// Endpoint is the base URL of the S3 service, without a path.
	Endpoint string
	// Bucket is the bucket the adapter may read and write.
	Bucket string
	// Region is the signing region. S3 requires one even for a local emulator.
	Region string
	// AccessKeyID is the credential identifier.
	AccessKeyID string
	// SecretAccessKey is a secret and must never be logged or echoed in an error.
	SecretAccessKey string
}

// Validate reports whether the configuration can work at all.
func (c Config) Validate() error {
	if _, err := parseEndpoint(c.Endpoint); err != nil {
		return err
	}
	if strings.TrimSpace(c.Bucket) == "" {
		return fmt.Errorf("%w: the bucket is empty", ErrNotConfigured)
	}
	if !validBucketName(c.Bucket) {
		return fmt.Errorf("%w: %q is not a valid S3 bucket name", ErrInvalidConfig, c.Bucket)
	}
	if strings.TrimSpace(c.Region) == "" {
		return fmt.Errorf("%w: the region is empty", ErrNotConfigured)
	}
	if strings.TrimSpace(c.AccessKeyID) == "" {
		return fmt.Errorf("%w: the access key id is empty", ErrNotConfigured)
	}
	if strings.TrimSpace(c.SecretAccessKey) == "" {
		// The value is never echoed, only its absence.
		return fmt.Errorf("%w: the secret access key is empty", ErrNotConfigured)
	}
	return nil
}

// S3 is the Store implementation backed by an S3-compatible service.
//
// The zero value is deliberately unusable: a caller that forgets NewS3 gets
// ErrNotConfigured from every method instead of a silent no-op.
type S3 struct {
	client    *s3.Client
	presigner *s3.PresignClient
	bucket    string
}

// NewS3 builds an S3-backed Store. It performs no I/O: the endpoint is contacted
// only by a later call, so a wrong configuration is reported at startup.
func NewS3(cfg Config) (*S3, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	endpoint, err := parseEndpoint(cfg.Endpoint)
	if err != nil {
		return nil, err
	}

	client := s3.NewFromConfig(aws.Config{
		Region:      cfg.Region,
		Credentials: credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
	}, func(o *s3.Options) {
		// Path-style addressing keeps "http://127.0.0.1:4566" working: the bucket
		// becomes a URL path segment instead of a virtual host.
		o.UsePathStyle = true
		o.BaseEndpoint = aws.String(endpoint)
	})

	return &S3{
		client:    client,
		presigner: s3.NewPresignClient(client),
		bucket:    cfg.Bucket,
	}, nil
}

// Put stores data under key. The object is written whole, and an existing object
// under the same key is replaced.
func (s *S3) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if err := s.usable(); err != nil {
		return err
	}
	if err := ValidateKey(key); err != nil {
		return err
	}
	if err := validateContentType(contentType); err != nil {
		return err
	}

	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
		ContentType:   aws.String(contentType),
	}); err != nil {
		return fmt.Errorf("storage: put %q: %w", key, err)
	}
	return nil
}

// Get reads the object stored under key.
func (s *S3) Get(ctx context.Context, key string) ([]byte, error) {
	if err := s.usable(); err != nil {
		return nil, err
	}
	if err := ValidateKey(key); err != nil {
		return nil, err
	}

	out, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("storage: get %q: %w", key, err)
	}
	defer func() { _ = out.Body.Close() }()

	data, err := io.ReadAll(out.Body)
	if err != nil {
		return nil, fmt.Errorf("storage: read %q: %w", key, err)
	}
	return data, nil
}

// PresignGet returns a pre-signed download URL for key that expires after ttl.
func (s *S3) PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error) {
	if err := s.usable(); err != nil {
		return "", err
	}
	if err := ValidateKey(key); err != nil {
		return "", err
	}
	if err := ValidatePresignTTL(ttl); err != nil {
		return "", err
	}

	req, err := s.presigner.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", fmt.Errorf("storage: presign %q: %w", key, err)
	}
	return req.URL, nil
}

// EnsureBucket creates the configured bucket when it does not exist yet.
//
// It is idempotent, so a startup path, a verification script and a test can all
// call it. It is not part of Store: only setup needs it, and a request handler
// must never create a bucket. A bucket that belongs to someone else is reported,
// never adopted.
func (s *S3) EnsureBucket(ctx context.Context) error {
	if err := s.usable(); err != nil {
		return err
	}

	if _, err := s.client.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(s.bucket)}); err == nil {
		return nil
	}

	// Any head failure, including a plain 404 from an emulator, is settled by
	// trying the create: a bucket that really is there answers "already owned by
	// you", which is the success case.
	if _, err := s.client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: aws.String(s.bucket)}); err != nil {
		if isBucketAlreadyUsable(err) {
			return nil
		}
		return fmt.Errorf("storage: ensure bucket %q: %w", s.bucket, err)
	}
	return nil
}

// usable reports whether the adapter was built by NewS3 with a bucket, so a zero
// value fails closed instead of dereferencing a nil client.
func (s *S3) usable() error {
	if s == nil || s.client == nil || s.presigner == nil {
		return fmt.Errorf("%w: the adapter was not built by NewS3", ErrNotConfigured)
	}
	if strings.TrimSpace(s.bucket) == "" {
		return fmt.Errorf("%w: the bucket is empty", ErrNotConfigured)
	}
	return nil
}

// ValidateKey reports whether key is a safe bucket-relative object name.
//
// The rules are deliberately narrow: an empty key, an absolute path, a prefix, a
// traversal segment, a backslash or a control character is refused, because each
// of them would let a signed URL address something other than the intended object.
func ValidateKey(key string) error {
	switch {
	case key == "":
		return fmt.Errorf("%w: the key is empty", ErrInvalidKey)
	case len(key) > maxKeyLength:
		return fmt.Errorf("%w: %d bytes exceeds the %d byte limit", ErrInvalidKey, len(key), maxKeyLength)
	case strings.HasPrefix(key, "/"):
		return fmt.Errorf("%w: %q is absolute, a key is relative to the bucket", ErrInvalidKey, key)
	case strings.HasSuffix(key, "/"):
		return fmt.Errorf("%w: %q names a prefix, not an object", ErrInvalidKey, key)
	case strings.Contains(key, "//"):
		return fmt.Errorf("%w: %q contains an empty segment", ErrInvalidKey, key)
	case strings.Contains(key, `\`):
		return fmt.Errorf("%w: %q contains a backslash", ErrInvalidKey, key)
	}

	for _, segment := range strings.Split(key, "/") {
		if segment == "." || segment == ".." {
			return fmt.Errorf("%w: %q contains a %q segment", ErrInvalidKey, key, segment)
		}
	}
	for _, r := range key {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: %q contains a control character", ErrInvalidKey, key)
		}
	}
	return nil
}

// ValidatePresignTTL reports whether ttl can be signed.
func ValidatePresignTTL(ttl time.Duration) error {
	if ttl < time.Second {
		// SigV4 carries whole seconds, so a shorter lifetime would be signed as
		// X-Amz-Expires=0 and the link could never work.
		return fmt.Errorf("%w: %s is shorter than the one-second floor a SigV4 signature needs", ErrInvalidTTL, ttl)
	}
	if ttl > maxPresignTTL {
		return fmt.Errorf("%w: %s exceeds the %s maximum SigV4 signature lifetime", ErrInvalidTTL, ttl, maxPresignTTL)
	}
	return nil
}

// validateContentType requires a media type: an object without one would be
// served back as an octet stream, and a download the browser cannot render is not
// the ticket the buyer asked for.
func validateContentType(contentType string) error {
	value := strings.TrimSpace(contentType)
	if value == "" {
		return fmt.Errorf("%w: the content type is empty", ErrInvalidContentType)
	}
	if !strings.Contains(value, "/") {
		return fmt.Errorf("%w: %q is not a media type such as application/pdf", ErrInvalidContentType, value)
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("%w: the content type contains a control character", ErrInvalidContentType)
		}
	}
	return nil
}

// parseEndpoint returns the endpoint without a trailing slash, or reports why it
// cannot be used. The endpoint must be an absolute http or https URL with no
// path: path-style addressing appends "/<bucket>/<key>" to it, so a prefix would
// silently become part of the object path.
func parseEndpoint(endpoint string) (string, error) {
	value := strings.TrimSpace(endpoint)
	if value == "" {
		return "", fmt.Errorf("%w: the endpoint is empty", ErrNotConfigured)
	}
	u, err := url.Parse(value)
	if err != nil {
		// The raw value may embed credentials, so it is never echoed here.
		return "", fmt.Errorf("%w: the endpoint does not parse as an absolute URL", ErrInvalidConfig)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("%w: the endpoint scheme %q is not http or https", ErrInvalidConfig, u.Scheme)
	}
	if u.Host == "" {
		return "", fmt.Errorf("%w: the endpoint has no host", ErrInvalidConfig)
	}
	if strings.Trim(u.Path, "/") != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", fmt.Errorf("%w: the endpoint must not carry a path, a query, a fragment or user information", ErrInvalidConfig)
	}
	return strings.TrimRight(value, "/"), nil
}

// validBucketName applies the S3 bucket naming rules that matter here: lower case
// letters, digits, dots and hyphens, three to sixty-three characters, starting and
// ending with a letter or a digit, no empty label and no address-shaped name.
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

// isBucketAlreadyUsable reports the create failures that mean the bucket is
// already there. Every other failure is a real configuration or access problem.
func isBucketAlreadyUsable(err error) bool {
	var ownedByYou *types.BucketAlreadyOwnedByYou
	if errors.As(err, &ownedByYou) {
		return true
	}
	// A bucket another account owns answers BucketAlreadyExists, which means the
	// name is taken and is not ours to use: only the owned-by-you code is a
	// success. An emulator may answer a bare 409 with the code in the body, so the
	// message is read as a fallback, for the owned-by-you code only.
	return strings.Contains(err.Error(), "BucketAlreadyOwnedByYou")
}
