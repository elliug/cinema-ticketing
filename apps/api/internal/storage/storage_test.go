// Unit tests for the object-storage boundary. They need no Docker: key
// validation, configuration rejection and the fail-closed behaviour of an
// unconfigured adapter are all decided before any request is made.
//
// The one behaviour only a running S3-compatible endpoint can prove — that a
// stored object reads back byte-identical and that a pre-signed URL really
// downloads it over plain HTTP until it expires — lives in the gated
// TestS3RoundTripAgainstFloci at the bottom of this file.
package storage_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/storage"
)

// testEndpoint is a syntactically valid endpoint that is deliberately closed, so
// a call that reaches the network fails with a transport error instead of the
// validation error under test.
const testEndpoint = "http://127.0.0.1:1"

// validConfig is the smallest configuration NewS3 accepts.
func validConfig() storage.Config {
	return storage.Config{
		Endpoint:        testEndpoint,
		Bucket:          "cinema-m3-tickets",
		Region:          "us-east-1",
		AccessKeyID:     "floci",
		SecretAccessKey: "floci",
	}
}

// TestValidateKey pins the key rules: a key is a bucket-relative object name, so
// an absolute path, a prefix, a traversal segment or a control character is
// refused before anything is signed or sent.
func TestValidateKey(t *testing.T) {
	longKey := strings.Repeat("a", 1030)
	cases := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"a ticket pdf key", "m3/tickets/9f3c/ticket.pdf", false},
		{"a single segment", "ticket.pdf", false},
		{"dashes and dots inside a segment", "m3/tickets/0001.ticket-2.pdf", false},
		{"empty key rejected", "", true},
		{"absolute key rejected", "/m3/ticket.pdf", true},
		{"prefix instead of an object rejected", "m3/tickets/", true},
		{"parent segment rejected", "m3/../ticket.pdf", true},
		{"current segment rejected", "m3/./ticket.pdf", true},
		{"doubled slash rejected", "m3//ticket.pdf", true},
		{"backslash rejected", `m3\tickets\ticket.pdf`, true},
		{"newline rejected", "m3/ticket\n.pdf", true},
		{"nul byte rejected", "m3/ticket\x00.pdf", true},
		{"key longer than the limit rejected", longKey, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := storage.ValidateKey(tc.key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateKey(%q) = nil, want an error", tc.key)
				}
				if !errors.Is(err, storage.ErrInvalidKey) {
					t.Fatalf("ValidateKey(%q) = %v, want ErrInvalidKey", tc.key, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateKey(%q) = %v, want nil", tc.key, err)
			}
		})
	}
}

// TestNewS3ValidatesConfiguration pins the fail-closed constructor: every
// combination that cannot work is refused, and no message echoes the secret.
func TestNewS3ValidatesConfiguration(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*storage.Config)
		wantErr error
	}{
		{
			name:    "the documented local configuration is accepted",
			mutate:  func(*storage.Config) {},
			wantErr: nil,
		},
		{
			name:    "an empty endpoint is not configured",
			mutate:  func(c *storage.Config) { c.Endpoint = "" },
			wantErr: storage.ErrNotConfigured,
		},
		{
			name:    "an empty bucket is not configured",
			mutate:  func(c *storage.Config) { c.Bucket = "" },
			wantErr: storage.ErrNotConfigured,
		},
		{
			name:    "an empty region is not configured",
			mutate:  func(c *storage.Config) { c.Region = "" },
			wantErr: storage.ErrNotConfigured,
		},
		{
			name:    "a missing access key is not configured",
			mutate:  func(c *storage.Config) { c.AccessKeyID = "" },
			wantErr: storage.ErrNotConfigured,
		},
		{
			name:    "a missing secret is not configured",
			mutate:  func(c *storage.Config) { c.SecretAccessKey = "" },
			wantErr: storage.ErrNotConfigured,
		},
		{
			name:    "an endpoint without a scheme is invalid",
			mutate:  func(c *storage.Config) { c.Endpoint = "127.0.0.1:4566" },
			wantErr: storage.ErrInvalidConfig,
		},
		{
			name:    "an endpoint with a path is invalid",
			mutate:  func(c *storage.Config) { c.Endpoint = "http://127.0.0.1:4566/prefix" },
			wantErr: storage.ErrInvalidConfig,
		},
		{
			name:    "an unsupported scheme is invalid",
			mutate:  func(c *storage.Config) { c.Endpoint = "ftp://127.0.0.1:4566" },
			wantErr: storage.ErrInvalidConfig,
		},
		{
			name:    "an upper case bucket is invalid",
			mutate:  func(c *storage.Config) { c.Bucket = "Cinema-M3-Tickets" },
			wantErr: storage.ErrInvalidConfig,
		},
		{
			name:    "a bucket that looks like an address is invalid",
			mutate:  func(c *storage.Config) { c.Bucket = "192.168.0.1" },
			wantErr: storage.ErrInvalidConfig,
		},
		{
			name:    "a too short bucket is invalid",
			mutate:  func(c *storage.Config) { c.Bucket = "m3" },
			wantErr: storage.ErrInvalidConfig,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig()
			tc.mutate(&cfg)
			store, err := storage.NewS3(cfg)
			if tc.wantErr != nil {
				if err == nil {
					t.Fatalf("NewS3 = nil error, want %v", tc.wantErr)
				}
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("NewS3 error = %v, want %v", err, tc.wantErr)
				}
				// The secret must never be echoed, whatever else is wrong.
				if strings.Contains(err.Error(), cfg.SecretAccessKey) && cfg.SecretAccessKey != "" {
					t.Fatalf("error leaked the secret: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("NewS3 returned error: %v", err)
			}
			if store == nil {
				t.Fatal("NewS3 returned a nil store without an error")
			}
		})
	}
}

// TestUnconfiguredStoreFailsClosed pins the requirement that an adapter that was
// never configured reports ErrNotConfigured rather than pretending to store or
// read an object, for every method of the boundary.
func TestUnconfiguredStoreFailsClosed(t *testing.T) {
	var nilStore *storage.S3
	zeroStore := &storage.S3{}
	ctx := context.Background()

	stores := map[string]*storage.S3{
		"a nil adapter":        nilStore,
		"a zero-value adapter": zeroStore,
	}

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			if err := store.Put(ctx, "m3/ticket.pdf", []byte("%PDF-1.4"), "application/pdf"); !errors.Is(err, storage.ErrNotConfigured) {
				t.Errorf("Put error = %v, want ErrNotConfigured", err)
			}
			if data, err := store.Get(ctx, "m3/ticket.pdf"); !errors.Is(err, storage.ErrNotConfigured) {
				t.Errorf("Get error = %v, want ErrNotConfigured", err)
			} else if data != nil {
				t.Errorf("Get data = %q, want nil", data)
			}
			if url, err := store.PresignGet(ctx, "m3/ticket.pdf", time.Minute); !errors.Is(err, storage.ErrNotConfigured) {
				t.Errorf("PresignGet error = %v, want ErrNotConfigured", err)
			} else if url != "" {
				t.Errorf("PresignGet url = %q, want empty", url)
			}
			if exists, err := store.Exists(ctx, "m3/ticket.pdf"); !errors.Is(err, storage.ErrNotConfigured) {
				t.Errorf("Exists error = %v, want ErrNotConfigured", err)
			} else if exists {
				t.Error("Exists = true, want false: an unconfigured adapter must never claim an object exists")
			}
			if err := store.EnsureBucket(ctx); !errors.Is(err, storage.ErrNotConfigured) {
				t.Errorf("EnsureBucket error = %v, want ErrNotConfigured", err)
			}
		})
	}
}

// TestInvalidInputIsRejectedBeforeAnyRequest pins that validation happens before
// signing or sending: the endpoint below is closed, so a request would fail with
// a transport error, and only a rejected input returns the validation error.
func TestInvalidInputIsRejectedBeforeAnyRequest(t *testing.T) {
	store, err := storage.NewS3(validConfig())
	if err != nil {
		t.Fatalf("NewS3 returned error: %v", err)
	}
	ctx := context.Background()

	if err := store.Put(ctx, "../escape.pdf", []byte("x"), "application/pdf"); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("Put error = %v, want ErrInvalidKey", err)
	}
	if _, err := store.Get(ctx, ""); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("Get error = %v, want ErrInvalidKey", err)
	}
	if _, err := store.PresignGet(ctx, "../escape.pdf", time.Minute); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("PresignGet error = %v, want ErrInvalidKey", err)
	}
	if _, err := store.Exists(ctx, "../escape.pdf"); !errors.Is(err, storage.ErrInvalidKey) {
		t.Fatalf("Exists error = %v, want ErrInvalidKey", err)
	}
	if err := store.Put(ctx, "m3/ticket.pdf", []byte("x"), ""); !errors.Is(err, storage.ErrInvalidContentType) {
		t.Fatalf("Put error = %v, want ErrInvalidContentType", err)
	}
	if err := store.Put(ctx, "m3/ticket.pdf", []byte("x"), "not-a-content-type"); !errors.Is(err, storage.ErrInvalidContentType) {
		t.Fatalf("Put error = %v, want ErrInvalidContentType", err)
	}
}

// TestPresignTTLIsBounded pins the lifetime rules: a signature that never
// expires is refused, and so is one longer than the seven days SigV4 allows.
func TestPresignTTLIsBounded(t *testing.T) {
	cases := []struct {
		name    string
		ttl     time.Duration
		wantErr bool
	}{
		{"one minute accepted", time.Minute, false},
		// SigV4 carries whole seconds: 500ms would be signed as X-Amz-Expires=0 and
		// the link would answer 400 before the object is ever fetched.
		{"one second is the floor", time.Second, false},
		{"sub-second rejected", 500 * time.Millisecond, true},
		{"seven days accepted", 7 * 24 * time.Hour, false},
		{"zero rejected", 0, true},
		{"negative rejected", -time.Minute, true},
		{"longer than seven days rejected", 7*24*time.Hour + time.Second, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := storage.ValidatePresignTTL(tc.ttl)
			if tc.wantErr {
				if !errors.Is(err, storage.ErrInvalidTTL) {
					t.Fatalf("ValidatePresignTTL(%v) = %v, want ErrInvalidTTL", tc.ttl, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidatePresignTTL(%v) = %v, want nil", tc.ttl, err)
			}
		})
	}
}

// TestExistsAsksTheStoreWithoutDownloading pins the one question the PDF archive
// asks before it renders anything: does this object already exist? A 200 answers
// yes and a 404 answers no, but every other answer is an error rather than a no —
// a store that could not be asked must never make the archive believe the artefact
// is missing and overwrite it with a re-render. The adapter asks with HEAD, so the
// question never downloads the bytes it is asking about.
func TestExistsAsksTheStoreWithoutDownloading(t *testing.T) {
	const key = "tickets/00000000-0000-0000-0000-0000000000e1/00000000-0000-0000-0000-0000000000e2.pdf"

	cases := []struct {
		name       string
		status     int
		wantExists bool
		wantErr    bool
	}{
		{"an existing object answers true", http.StatusOK, true, false},
		{"a missing object answers false", http.StatusNotFound, false, false},
		// A 403 is not proof of absence, so it is reported instead of answered.
		{"an unreadable bucket is an error", http.StatusForbidden, false, true},
		{"a store failure is an error", http.StatusInternalServerError, false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var methods []string
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				methods = append(methods, r.Method)
				paths = append(paths, r.URL.Path)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			store, err := storage.NewS3(storage.Config{Endpoint: srv.URL, Bucket: "cinema-m3-tickets",
				Region: "us-east-1", AccessKeyID: "a", SecretAccessKey: "b"})
			if err != nil {
				t.Fatalf("NewS3 returned error: %v", err)
			}

			exists, err := store.Exists(context.Background(), key)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("Exists = (%v, nil), want an error: a store that could not be asked answered %v", exists, tc.status)
				}
				if exists {
					t.Fatal("Exists = true on an error, want false")
				}
			} else {
				if err != nil {
					t.Fatalf("Exists returned error: %v", err)
				}
				if exists != tc.wantExists {
					t.Fatalf("Exists = %v, want %v", exists, tc.wantExists)
				}
			}

			// The SDK retries a 5xx, so the count is not pinned; what matters is that the
			// question is asked with HEAD and never with GET, so no bytes are downloaded.
			if len(methods) == 0 {
				t.Fatal("no request reached the store, want at least one HEAD")
			}
			for _, method := range methods {
				if method != http.MethodHead {
					t.Fatalf("requests = %v, want only HEAD: exists must not download the object", methods)
				}
			}
			if want := "/cinema-m3-tickets/" + key; paths[0] != want {
				t.Fatalf("path = %q, want %q", paths[0], want)
			}
		})
	}
}

// Gated S3 evidence. Set M3_S3_ENDPOINT and M3_S3_BUCKET to run it against a
// running Floci container (the compose service publishes http://127.0.0.1:4566);
// the default unit suite skips it and needs no Docker. Credentials default to the
// dummy pair Floci accepts and can be overridden with M3_S3_ACCESS_KEY_ID,
// M3_S3_SECRET_ACCESS_KEY and M3_S3_REGION.
const (
	s3EndpointEnv  = "M3_S3_ENDPOINT"
	s3BucketEnv    = "M3_S3_BUCKET"
	s3RegionEnv    = "M3_S3_REGION"
	s3AccessKeyEnv = "M3_S3_ACCESS_KEY_ID"
	s3SecretKeyEnv = "M3_S3_SECRET_ACCESS_KEY"

	// testBucketPrefix is the only bucket name space this test may create or
	// write to, so it can never touch a bucket that already held data.
	testBucketPrefix = "cinema-m3-"

	// testObjectKey is fixed so repeated runs overwrite their own object instead
	// of accumulating keys in the bucket.
	testObjectKey = "m3-t0/storage-selftest.pdf"
)

// TestS3RoundTripAgainstFloci is the only test that proves the adapter speaks
// S3: the bucket exists (it is created when missing), an object put through the
// boundary reads back byte-identical, and a pre-signed URL converts into a plain
// HTTP download that stops working once its lifetime has elapsed.
func TestS3RoundTripAgainstFloci(t *testing.T) {
	endpoint := strings.TrimSpace(os.Getenv(s3EndpointEnv))
	bucket := strings.TrimSpace(os.Getenv(s3BucketEnv))
	if endpoint == "" || bucket == "" {
		t.Skipf("%s and %s are not set: this test needs a running S3-compatible endpoint (make deps-up starts Floci)", s3EndpointEnv, s3BucketEnv)
	}
	if !strings.HasPrefix(bucket, testBucketPrefix) {
		t.Fatalf("%s = %q, want a name starting with %q so this test can only touch its own bucket", s3BucketEnv, bucket, testBucketPrefix)
	}

	region := strings.TrimSpace(os.Getenv(s3RegionEnv))
	if region == "" {
		region = "us-east-1"
	}
	accessKeyID := strings.TrimSpace(os.Getenv(s3AccessKeyEnv))
	secretAccessKey := strings.TrimSpace(os.Getenv(s3SecretKeyEnv))
	if accessKeyID == "" {
		accessKeyID = "floci"
	}
	if secretAccessKey == "" {
		secretAccessKey = "floci"
	}

	ctx := t.Context()
	store, err := storage.NewS3(storage.Config{
		Endpoint:        endpoint,
		Bucket:          bucket,
		Region:          region,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secretAccessKey,
	})
	if err != nil {
		t.Fatalf("NewS3 returned error: %v", err)
	}
	if err := store.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket(%q) returned error: %v", bucket, err)
	}
	t.Logf("bucket %q exists at %s (created when missing)", bucket, endpoint)

	// The payload carries every byte value and a NUL run, so a truncated or
	// re-encoded object cannot pass for an identical one.
	payload := make([]byte, 0, 2048)
	payload = append(payload, "%PDF-1.4\n"...)
	for i := 0; i < 4; i++ {
		for b := 0; b < 256; b++ {
			payload = append(payload, byte(b))
		}
	}
	payload = append(payload, "\n%%EOF\n"...)

	// Exists is the question the PDF archive asks before it renders: a missing key
	// answers false, and only a stored object answers true.
	if exists, err := store.Exists(ctx, testObjectKey); err != nil {
		t.Fatalf("Exists(%q) returned error: %v", testObjectKey, err)
	} else if exists {
		t.Fatalf("Exists(%q) = true before the object was put", testObjectKey)
	}

	if err := store.Put(ctx, testObjectKey, payload, "application/pdf"); err != nil {
		t.Fatalf("Put(%q) returned error: %v", testObjectKey, err)
	}
	t.Logf("Put stored %d bytes at %q", len(payload), testObjectKey)

	if exists, err := store.Exists(ctx, testObjectKey); err != nil {
		t.Fatalf("Exists(%q) returned error: %v", testObjectKey, err)
	} else if !exists {
		t.Fatalf("Exists(%q) = false after the object was put", testObjectKey)
	}
	t.Logf("Exists(%q) = true after the put", testObjectKey)

	stored, err := store.Get(ctx, testObjectKey)
	if err != nil {
		t.Fatalf("Get(%q) returned error: %v", testObjectKey, err)
	}
	if !bytes.Equal(stored, payload) {
		t.Fatalf("Get(%q) returned %d bytes that differ from the %d stored bytes", testObjectKey, len(stored), len(payload))
	}
	t.Logf("Get read back %d identical bytes", len(stored))

	// A two second lifetime is long enough for the first download and short
	// enough to watch it stop working.
	const ttl = 2 * time.Second
	presigned, err := store.PresignGet(ctx, testObjectKey, ttl)
	if err != nil {
		t.Fatalf("PresignGet(%q) returned error: %v", testObjectKey, err)
	}
	parsed, err := url.Parse(presigned)
	if err != nil {
		t.Fatalf("pre-signed URL does not parse: %v", err)
	}
	if parsed.Scheme != "http" {
		t.Fatalf("pre-signed URL scheme = %q, want plain http so the download needs no TLS", parsed.Scheme)
	}
	if parsed.Host != strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://") {
		t.Fatalf("pre-signed URL host = %q, want the configured endpoint", parsed.Host)
	}
	if got := parsed.Query().Get("X-Amz-Expires"); got != "2" {
		t.Fatalf("X-Amz-Expires = %q, want 2", got)
	}
	if parsed.Query().Get("X-Amz-Signature") == "" {
		t.Fatal("pre-signed URL carries no X-Amz-Signature")
	}
	t.Logf("presigned %s%s with a %s lifetime", parsed.Scheme+"://"+parsed.Host, parsed.Path, ttl)

	client := &http.Client{Timeout: 10 * time.Second}
	issuedAt := time.Now()
	status, body := download(t, client, presigned)
	if status != http.StatusOK {
		t.Fatalf("pre-signed download status = %d, want 200 (body: %s)", status, tail(body))
	}
	if !bytes.Equal(body, payload) {
		t.Fatalf("pre-signed download returned %d bytes that differ from the %d stored bytes", len(body), len(payload))
	}
	t.Logf("pre-signed download returned 200 with %d identical bytes", len(body))

	// Wait out the lifetime; a pre-signed URL must stop working on its own.
	deadline := issuedAt.Add(ttl + time.Second)
	if wait := time.Until(deadline); wait > 0 {
		time.Sleep(wait)
	}
	status, body = download(t, client, presigned)
	if status == http.StatusOK {
		t.Fatalf("pre-signed download still returned 200 after its %s lifetime: %s", ttl, tail(body))
	}
	t.Logf("pre-signed download returned %d after the lifetime elapsed (body: %s)", status, tail(body))
}

// download performs one plain HTTP GET and returns the status and the body.
func download(t *testing.T, client *http.Client, url string) (int, []byte) {
	t.Helper()
	resp, err := client.Get(url)
	if err != nil {
		t.Fatalf("pre-signed download failed: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read the pre-signed download: %v", err)
	}
	return resp.StatusCode, body
}

// tail keeps a failure message readable for a large body.
func tail(body []byte) string {
	const limit = 200
	if len(body) <= limit {
		return string(body)
	}
	return fmt.Sprintf("...%s", string(body[len(body)-limit:]))
}

// D1/S5: EnsureBucket must report a bucket that belongs to somebody else.
func TestEnsureBucketReportsAForeignBucket(t *testing.T) {
	for _, tc := range []struct {
		name      string
		head      int
		code      string
		wantError bool
	}{
		{"a bucket that answers head 200 needs no create", http.StatusOK, "", false},
		{"a bucket another account owns is reported", http.StatusForbidden, "BucketAlreadyExists", true},
		{"a create refused with access denied is reported", http.StatusForbidden, "AccessDenied", true},
		{"a bucket we already own is usable", http.StatusForbidden, "BucketAlreadyOwnedByYou", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				seen = append(seen, r.Method)
				if r.Method == http.MethodHead {
					w.WriteHeader(tc.head)
					return
				}
				w.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(w, "<Error><Code>"+tc.code+"</Code></Error>")
			}))
			defer srv.Close()

			store, err := storage.NewS3(storage.Config{Endpoint: srv.URL, Bucket: "cinema-m3-tickets",
				Region: "us-east-1", AccessKeyID: "a", SecretAccessKey: "b"})
			if err != nil {
				t.Fatalf("NewS3 returned error: %v", err)
			}
			err = store.EnsureBucket(context.Background())
			if tc.wantError && err == nil {
				t.Fatalf("EnsureBucket = nil, want an error: the adapter adopted a bucket it does not own (requests %v)", seen)
			}
			if !tc.wantError && err != nil {
				t.Fatalf("EnsureBucket = %v, want nil", err)
			}
			if tc.head == http.StatusOK && len(seen) != 1 {
				t.Fatalf("an existing bucket was modified: requests %v", seen)
			}
		})
	}
}

// D2: a lifetime SigV4 cannot carry is refused, not signed as X-Amz-Expires=0.
func TestPresignTTLRejectsSubSecondLifetimes(t *testing.T) {
	for _, ttl := range []time.Duration{time.Millisecond, 999 * time.Millisecond, 0, -time.Second} {
		if err := storage.ValidatePresignTTL(ttl); !errors.Is(err, storage.ErrInvalidTTL) {
			t.Errorf("ValidatePresignTTL(%v) = %v, want ErrInvalidTTL (a whole-second floor is required)", ttl, err)
		}
	}
	if err := storage.ValidatePresignTTL(time.Second); err != nil {
		t.Errorf("ValidatePresignTTL(1s) = %v, want nil", err)
	}
}
