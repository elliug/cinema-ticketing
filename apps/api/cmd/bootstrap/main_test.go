package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/storage"
)

// environment returns a deterministic getenv over a map.
func environment(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

// constantDeps builds the dependencies every invocation of run receives.
func constantDeps(d dependencies) func(string) (dependencies, error) {
	return func(string) (dependencies, error) { return d, nil }
}

func execute(t *testing.T, args []string, values map[string]string, d dependencies) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(context.Background(), args, environment(values), &out, &errOut, constantDeps(d))
	return code, out.String(), errOut.String()
}

// unusedProbe fails the test when a command that must refuse early still probes.
func unusedProbe(t *testing.T) func(context.Context, string) error {
	t.Helper()
	return func(context.Context, string) error {
		t.Fatalf("no service may be probed")
		return nil
	}
}

// none — a missing or unknown command is a command-line misuse, so it exits 2
// with the usage on stderr and never touches a dependency.
func TestCommandLineMisuse(t *testing.T) {
	for _, testCase := range []struct {
		name string
		args []string
		want string
	}{
		{name: "no command", args: nil, want: "usage: bootstrap"},
		{name: "unknown command", args: []string{"nope"}, want: `unknown command "nope"`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			code, out, errOut := execute(t, testCase.args, nil, dependencies{})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if !strings.Contains(errOut, testCase.want) {
				t.Fatalf("stderr = %q, want it to contain %q", errOut, testCase.want)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want it empty", out)
			}
		})
	}
}

func TestHelpGoesToStdout(t *testing.T) {
	code, out, errOut := execute(t, []string{"help"}, nil, dependencies{})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.Contains(out, "usage: bootstrap") {
		t.Fatalf("stdout = %q, want the usage", out)
	}
	if errOut != "" {
		t.Fatalf("stderr = %q, want it empty", errOut)
	}
}

// wait-deps — a bounded timeout is the only accepted argument, and every refusal
// happens before a single probe runs.
func TestWaitDepsRejectsBadArguments(t *testing.T) {
	for _, args := range [][]string{
		{"--timeout", "0"},
		{"--timeout", "-5s"},
		{"--timeout", "soon"},
		{"--timeout"},
		{"--timeout", "5s", "extra"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			code, out, errOut := execute(t, append([]string{"wait-deps"}, args...), nil,
				dependencies{probe: unusedProbe(t)})
			if code != 2 {
				t.Fatalf("exit code = %d, want 2", code)
			}
			if errOut == "" {
				t.Fatal("stderr is empty, want the reason")
			}
			if out != "" {
				t.Fatalf("stdout = %q, want it empty", out)
			}
		})
	}
}

// wait-deps — the services are probed in order and each one is reported.
func TestWaitDepsReportsEveryService(t *testing.T) {
	var probed []string
	d := dependencies{
		retry: time.Millisecond,
		probe: func(_ context.Context, service string) error {
			probed = append(probed, service)
			return nil
		},
	}
	code, out, errOut := execute(t, []string{"wait-deps", "--timeout", "5s"}, nil, d)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut)
	}
	if got, want := strings.Join(probed, ","), "postgres,redis,floci"; got != want {
		t.Fatalf("probed %q, want %q", got, want)
	}
	for _, service := range []string{"postgres", "redis", "floci"} {
		if !strings.Contains(out, service+" is ready") {
			t.Fatalf("stdout = %q, want %q reported", out, service)
		}
	}
}

// wait-deps — a service that is slow is retried instead of failing the wait.
func TestWaitDepsRetriesUntilReady(t *testing.T) {
	attempts := map[string]int{}
	d := dependencies{
		retry: time.Millisecond,
		probe: func(_ context.Context, service string) error {
			attempts[service]++
			if service == "redis" && attempts[service] < 3 {
				return errors.New("connection refused")
			}
			return nil
		},
	}
	code, _, errOut := execute(t, []string{"wait-deps", "--timeout", "5s"}, nil, d)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut)
	}
	if attempts["redis"] != 3 {
		t.Fatalf("redis probed %d times, want 3", attempts["redis"])
	}
}

// wait-deps — a service that never answers fails the target and is named, with
// the bounded timeout the caller asked for and the last probe error.
func TestWaitDepsNamesTheServiceThatIsNotReady(t *testing.T) {
	d := dependencies{
		retry: 5 * time.Millisecond,
		probe: func(_ context.Context, service string) error {
			if service == "floci" {
				return errors.New("connection refused")
			}
			return nil
		},
	}
	code, out, errOut := execute(t, []string{"wait-deps", "--timeout", "80ms"}, nil, d)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	want := "bootstrap: floci is not ready after 80ms: connection refused"
	if !strings.Contains(errOut, want) {
		t.Fatalf("stderr = %q, want it to contain %q", errOut, want)
	}
	if strings.Contains(out, "floci is ready") {
		t.Fatalf("stdout = %q, want it to omit the failing service", out)
	}
}

// wait-deps — an unusable environment fails closed instead of waiting.
func TestWaitDepsFailsClosedWithoutProbeOrCompose(t *testing.T) {
	code, out, errOut := execute(t, []string{"wait-deps"}, nil, dependencies{})
	if code != 1 || out != "" || !strings.Contains(errOut, "no readiness probe") {
		t.Fatalf("missing probe: code=%d out=%q err=%q", code, out, errOut)
	}

	var out2, err2 bytes.Buffer
	code = run(context.Background(), []string{"wait-deps"}, environment(nil), &out2, &err2,
		func(string) (dependencies, error) { return dependencies{}, errors.New("Docker Compose is required") })
	if code != 1 || out2.String() != "" || !strings.Contains(err2.String(), "Docker Compose is required") {
		t.Fatalf("missing compose: code=%d out=%q err=%q", code, out2.String(), err2.String())
	}
}

// seed-dev — the development guard is checked before anything connects, so an
// unset or different API_ENV can never seed a database.
func TestSeedDevRefusesOutsideDevelopment(t *testing.T) {
	for _, environment := range []map[string]string{
		nil,
		{"API_ENV": ""},
		{"API_ENV": "production"},
		{"API_ENV": "Development"},
		{"API_ENV": "staging"},
	} {
		t.Run(environment["API_ENV"], func(t *testing.T) {
			seeded := false
			d := dependencies{seed: func(context.Context, string) error {
				seeded = true
				return nil
			}}
			code, out, errOut := execute(t, []string{"seed-dev"}, environment, d)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if seeded {
				t.Fatal("the seed ran outside development")
			}
			if !strings.Contains(errOut, "API_ENV=development") {
				t.Fatalf("stderr = %q, want the guard named", errOut)
			}
			if out != "" {
				t.Fatalf("stdout = %q, want it empty", out)
			}
		})
	}
}

func TestSeedDevAppliesTheSeedFile(t *testing.T) {
	var applied string
	d := dependencies{seed: func(_ context.Context, path string) error {
		applied = path
		return nil
	}}
	code, out, errOut := execute(t, []string{"seed-dev"}, map[string]string{"API_ENV": "development"}, d)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut)
	}
	if applied != "db/seeds/dev_seed.sql" {
		t.Fatalf("seeded %q, want db/seeds/dev_seed.sql", applied)
	}
	if !strings.Contains(out, "db/seeds/dev_seed.sql") {
		t.Fatalf("stdout = %q, want the seed named", out)
	}
}

func TestSeedDevReportsFailure(t *testing.T) {
	d := dependencies{seed: func(context.Context, string) error { return errors.New("permission denied") }}
	code, _, errOut := execute(t, []string{"seed-dev"}, map[string]string{"API_ENV": "development"}, d)
	if code != 1 || !strings.Contains(errOut, "permission denied") {
		t.Fatalf("code=%d err=%q, want 1 and the failure", code, errOut)
	}
}

// ensure-bucket — a configuration that cannot work fails closed before the
// endpoint is contacted, and nothing secret reaches the output.
func TestEnsureBucketFailsClosedOnBadConfiguration(t *testing.T) {
	for _, testCase := range []struct {
		name string
		env  map[string]string
	}{
		{name: "half a credential pair", env: map[string]string{
			"API_S3_ACCESS_KEY_ID": "", "API_S3_SECRET_ACCESS_KEY": "s3cr3t-value"}},
		{name: "an invalid bucket name", env: map[string]string{"API_S3_BUCKET": "Not_A_Bucket"}},
		{name: "a development credential with a remote endpoint", env: map[string]string{
			"API_S3_ENDPOINT": "https://s3.us-east-1.amazonaws.com"}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ensured := false
			d := dependencies{ensureBucket: func(context.Context, config.Config) (string, error) {
				ensured = true
				return "", nil
			}}
			code, out, errOut := execute(t, []string{"ensure-bucket"}, testCase.env, d)
			if code != 1 {
				t.Fatalf("exit code = %d, want 1", code)
			}
			if ensured {
				t.Fatal("the bucket adapter ran with an unusable configuration")
			}
			if errOut == "" || out != "" {
				t.Fatalf("out=%q err=%q, want the reason on stderr only", out, errOut)
			}
			if strings.Contains(out+errOut, "s3cr3t-value") {
				t.Fatalf("the output echoed the secret: %q", out+errOut)
			}
		})
	}
}

// ensure-bucket — an unconfigured adapter is reported, never reported as success.
func TestEnsureBucketReportsAnUnconfiguredStore(t *testing.T) {
	d := dependencies{ensureBucket: func(context.Context, config.Config) (string, error) {
		return "", storage.ErrNotConfigured
	}}
	code, out, errOut := execute(t, []string{"ensure-bucket"}, nil, d)
	if code != 1 {
		t.Fatalf("exit code = %d, want 1", code)
	}
	if !strings.Contains(errOut, "object storage is not configured") {
		t.Fatalf("stderr = %q, want the not-configured reason", errOut)
	}
	if out != "" {
		t.Fatalf("stdout = %q, want it empty", out)
	}
}

// ensure-bucket — the bucket name is the only thing printed, while the secret is
// handed to the adapter and never echoed.
func TestEnsureBucketPrintsOnlyTheBucketName(t *testing.T) {
	const secret = "s3cr3t-value"
	var received config.Config
	d := dependencies{ensureBucket: func(_ context.Context, cfg config.Config) (string, error) {
		received = cfg
		return cfg.S3Bucket, nil
	}}
	env := map[string]string{
		"API_S3_BUCKET":            "cinema-m3-test-bucket",
		"API_S3_ACCESS_KEY_ID":     "test-key",
		"API_S3_SECRET_ACCESS_KEY": secret,
	}
	code, out, errOut := execute(t, []string{"ensure-bucket"}, env, d)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0 (stderr: %s)", code, errOut)
	}
	if out != "bucket cinema-m3-test-bucket is ready\n" {
		t.Fatalf("stdout = %q, want only the bucket name", out)
	}
	if received.S3SecretAccessKey != secret {
		t.Fatal("the adapter did not receive the configured secret")
	}
	if strings.Contains(out+errOut, secret) {
		t.Fatalf("the output echoed the secret: %q", out+errOut)
	}
}

// ensure-bucket — the real adapter refuses an incomplete configuration without
// contacting the endpoint, and its error carries no credential.
func TestLiveEnsureBucketNeverEchoesTheSecret(t *testing.T) {
	const secret = "s3cr3t-value"
	_, err := liveEnsureBucket(context.Background(), config.Config{
		S3Endpoint:        "http://127.0.0.1:4566",
		S3Bucket:          "",
		S3Region:          "us-east-1",
		S3AccessKeyID:     "test-key",
		S3SecretAccessKey: secret,
	})
	if err == nil {
		t.Fatal("an empty bucket was accepted")
	}
	if !errors.Is(err, storage.ErrNotConfigured) {
		t.Fatalf("err = %v, want ErrNotConfigured", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error echoed the secret: %v", err)
	}
}
