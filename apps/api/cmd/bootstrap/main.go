// Command bootstrap prepares the local development environment for the M3
// checkout slice.
//
// It is the only command here that creates the shared state the rest of the
// milestone assumes exists: a ready dependency set, an applied migration chain,
// the development seed rows and the ticket bucket. Every subcommand is
// re-runnable and fails closed:
//
//	wait-deps      bounded readiness wait for postgres, redis and floci; it only
//	               reads, and it names the service that is not ready
//	seed-dev       applies db/seeds/dev_seed.sql and refuses to run unless
//	               API_ENV=development is explicitly set
//	ensure-bucket  loads the validated configuration and creates the configured
//	               bucket when it is missing, printing the bucket name and never
//	               the credentials
//
// The side-effecting operations sit behind dependencies, so every decision the
// command makes is exercised in tests without Docker, a database or S3.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/storage"
)

const (
	// defaultWaitTimeout bounds the whole readiness wait, so `make up` can never
	// hang on a dependency that will not come up.
	defaultWaitTimeout = 60 * time.Second
	// defaultRetryInterval is the pause between two probes of one service.
	defaultRetryInterval = time.Second
	// probeTimeout bounds a single probe, so one hung exec or socket cannot eat
	// the whole wait budget.
	probeTimeout = 5 * time.Second
	// seedFile is the development seed applied by seed-dev, relative to the
	// repository root the Makefile runs from.
	seedFile = "db/seeds/dev_seed.sql"
	// development is the only environment seed-dev runs in.
	development = "development"
	// defaultDatabase and defaultDatabaseUser mirror the compose defaults, so the
	// probes and the seed address the database the services were started with.
	defaultDatabase     = "cinema"
	defaultDatabaseUser = "cinema"
)

// readyServices are probed in this order. The names are the compose service
// names, so a failure names something the operator can restart.
var readyServices = []string{"postgres", "redis", "floci"}

const usageText = `usage: bootstrap <command>

Prepares the local development environment. Every command is re-runnable.

commands:
  wait-deps [--timeout 1m]  wait for postgres, redis and floci, naming the one that is not ready
  seed-dev                  apply db/seeds/dev_seed.sql; requires API_ENV=development
  ensure-bucket             create the configured ticket bucket when it is missing

environment:
  API_ENV                     must be development for seed-dev
  POSTGRES_DB, POSTGRES_USER  the database the probes and the seed address (default cinema)
  API_S3_*                    the object-storage settings read by ensure-bucket
`

// dependencies are the side-effecting operations of the command. Tests replace
// them, so the command's decisions are observable without Docker, a database or
// an object store.
type dependencies struct {
	// probe reports whether one compose service is ready.
	probe func(ctx context.Context, service string) error
	// seed applies the seed file to the development database.
	seed func(ctx context.Context, path string) error
	// ensureBucket makes the configured bucket exist and returns its name.
	ensureBucket func(ctx context.Context, cfg config.Config) (string, error)
	// retry is the pause between two probes of the same service.
	retry time.Duration
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Getenv, os.Stdout, os.Stderr, live))
}

// run dispatches one command and returns its exit code: 0 for success, 1 for a
// runtime failure and 2 for a misuse of the command line.
func run(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer,
	build func(string) (dependencies, error)) int {
	if len(args) == 0 {
		fmt.Fprint(errOut, usageText)
		return 2
	}
	command, rest := args[0], args[1:]
	switch command {
	case "help", "-h", "--help":
		fmt.Fprint(out, usageText)
		return 0
	case "wait-deps":
		return waitDeps(ctx, rest, out, errOut, build)
	case "seed-dev":
		return seedDev(ctx, rest, getenv, out, errOut, build)
	case "ensure-bucket":
		return ensureBucket(ctx, rest, getenv, out, errOut, build)
	}
	fmt.Fprintf(errOut, "bootstrap: unknown command %q\n\n", command)
	fmt.Fprint(errOut, usageText)
	return 2
}

// waitDeps is the explicit readiness wait `make up` runs after the containers
// have started. It reads only: no object is written and no table is touched.
func waitDeps(ctx context.Context, args []string, out, errOut io.Writer,
	build func(string) (dependencies, error)) int {
	timeout, ok := parseWaitArgs(args, errOut)
	if !ok {
		return 2
	}
	d, err := build("wait-deps")
	if err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	if d.probe == nil {
		fmt.Fprint(errOut, "bootstrap: no readiness probe is configured\n")
		return 1
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	for _, service := range readyServices {
		if err := waitFor(waitCtx, d, service); err != nil {
			fmt.Fprintf(errOut, "bootstrap: %s is not ready after %s: %v\n", service, timeout, err)
			return 1
		}
		fmt.Fprintf(out, "%s is ready\n", service)
	}
	fmt.Fprintf(out, "dependencies are ready (waited at most %s)\n", timeout)
	return 0
}

// parseWaitArgs reads the bounded timeout. A malformed or non-positive timeout is
// a usage error, never a silent fallback to the default.
func parseWaitArgs(args []string, errOut io.Writer) (time.Duration, bool) {
	fs := flag.NewFlagSet("wait-deps", flag.ContinueOnError)
	fs.SetOutput(errOut)
	timeout := fs.Duration("timeout", defaultWaitTimeout, "how long the whole wait may take")
	fs.Usage = func() { fmt.Fprint(errOut, usageText) }
	if err := fs.Parse(args); err != nil {
		return 0, false
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(errOut, "bootstrap: wait-deps takes no positional argument, got %q\n", fs.Arg(0))
		return 0, false
	}
	if *timeout <= 0 {
		fmt.Fprintf(errOut, "bootstrap: --timeout must be positive, got %s\n", *timeout)
		return 0, false
	}
	return *timeout, true
}

// waitFor probes one service until it answers or the wait budget is spent. The
// last failure is returned, so the caller can name both the service and why.
func waitFor(ctx context.Context, d dependencies, service string) error {
	retry := d.retry
	if retry <= 0 {
		retry = defaultRetryInterval
	}
	var last error
	for {
		probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
		last = d.probe(probeCtx, service)
		cancel()
		if last == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return last
		case <-time.After(retry):
		}
	}
}

// seedDev applies the development seed. The environment guard runs before any
// connection is opened, so an explicit API_ENV=development is the only way in.
func seedDev(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer,
	build func(string) (dependencies, error)) int {
	if len(args) > 0 {
		fmt.Fprintf(errOut, "bootstrap: seed-dev takes no argument, got %q\n", args[0])
		return 2
	}
	environment := getenv("API_ENV")
	if environment != development {
		fmt.Fprintf(errOut, "bootstrap: seed-dev refuses to run unless API_ENV=development is explicitly set (API_ENV=%q)\n", environment)
		return 1
	}
	d, err := build("seed-dev")
	if err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	if d.seed == nil {
		fmt.Fprint(errOut, "bootstrap: no seed runner is configured\n")
		return 1
	}
	if err := d.seed(ctx, seedFile); err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "seeded %s\n", seedFile)
	return 0
}

// ensureBucket creates the configured ticket bucket when it is missing. Only the
// bucket name is printed: the configuration also carries a secret.
func ensureBucket(ctx context.Context, args []string, getenv func(string) string, out, errOut io.Writer,
	build func(string) (dependencies, error)) int {
	if len(args) > 0 {
		fmt.Fprintf(errOut, "bootstrap: ensure-bucket takes no argument, got %q\n", args[0])
		return 2
	}
	cfg, err := config.LoadFrom(getenv)
	if err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	d, err := build("ensure-bucket")
	if err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	if d.ensureBucket == nil {
		fmt.Fprint(errOut, "bootstrap: no bucket adapter is configured\n")
		return 1
	}
	callCtx, cancel := context.WithTimeout(ctx, defaultWaitTimeout)
	defer cancel()
	name, err := d.ensureBucket(callCtx, cfg)
	if err != nil {
		fmt.Fprintf(errOut, "bootstrap: %v\n", err)
		return 1
	}
	fmt.Fprintf(out, "bucket %s is ready\n", name)
	return 0
}

// live builds the real dependencies of one command. Only the commands that drive
// Docker Compose resolve it here, so a configuration-only failure or `help` never
// depends on a running daemon.
func live(command string) (dependencies, error) {
	d := dependencies{retry: defaultRetryInterval}
	if command == "ensure-bucket" {
		d.ensureBucket = liveEnsureBucket
		return d, nil
	}
	runner, err := newComposeRunner()
	if err != nil {
		return dependencies{}, err
	}
	switch command {
	case "wait-deps":
		cfg, err := config.LoadFrom(os.Getenv)
		if err != nil {
			return dependencies{}, err
		}
		d.probe = composeProbe(runner, cfg, os.Getenv)
	case "seed-dev":
		d.seed = psqlSeed(runner, os.Getenv)
	}
	return d, nil
}

// composeRunner runs the Docker Compose CLI this machine provides. Both the
// `docker compose` plugin and the `docker-compose` binary are supported, as in the
// Makefile.
type composeRunner struct {
	bin  string
	args []string
}

func newComposeRunner() (composeRunner, error) {
	if _, err := exec.LookPath("docker"); err == nil {
		if exec.Command("docker", "compose", "version").Run() == nil {
			return composeRunner{bin: "docker", args: []string{"compose"}}, nil
		}
	}
	if _, err := exec.LookPath("docker-compose"); err == nil {
		return composeRunner{bin: "docker-compose"}, nil
	}
	return composeRunner{}, errors.New("Docker Compose is required")
}

func (c composeRunner) run(ctx context.Context, args ...string) ([]byte, error) {
	line := append(append([]string{}, c.args...), args...)
	cmd := exec.CommandContext(ctx, c.bin, line...)
	combined, err := cmd.CombinedOutput()
	if err == nil {
		return combined, nil
	}
	if message := strings.TrimSpace(string(combined)); message != "" {
		return combined, fmt.Errorf("%s", message)
	}
	return combined, err
}

// composeProbe probes the three local services: PostgreSQL with pg_isready inside
// its own container, Redis with PING over its URL, and Floci with its own health
// endpoint from the host.
func composeProbe(runner composeRunner, cfg config.Config, getenv func(string) string) func(context.Context, string) error {
	client := &http.Client{Timeout: probeTimeout}
	return func(ctx context.Context, service string) error {
		switch service {
		case "postgres":
			_, err := runner.run(ctx, "exec", "-T", "postgres", "pg_isready",
				"-U", databaseUser(getenv), "-d", databaseName(getenv))
			return err
		case "redis":
			return pingRedis(ctx, cfg.RedisURL)
		case "floci":
			return pingFloci(ctx, client, cfg.S3Endpoint)
		}
		return fmt.Errorf("unknown service %q", service)
	}
}

// pingRedis is the Redis readiness check: a PING over the configured URL. Only
// the address is reported, because the URL may carry a password.
func pingRedis(ctx context.Context, redisURL string) error {
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		return fmt.Errorf("parse the coordination URL: %w", err)
	}
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("PING %s: %w", options.Addr, err)
	}
	return nil
}

// pingFloci asks the emulator's own health endpoint, the same probe the other
// verification scripts use.
func pingFloci(ctx context.Context, client *http.Client, endpoint string) error {
	url := strings.TrimRight(endpoint, "/") + "/_floci/health"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s answered %s", url, resp.Status)
	}
	return nil
}

// psqlSeed applies the seed file with psql inside the postgres container, so the
// seed does not have to exist on the host and the run uses the same client the
// migrations do. The file is idempotent on its own; this only carries it over.
func psqlSeed(runner composeRunner, getenv func(string) string) func(context.Context, string) error {
	return func(ctx context.Context, path string) error {
		sql, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read the seed: %w", err)
		}
		line := append(append([]string{}, runner.args...),
			"exec", "-T", "postgres", "psql", "-X", "-q", "-v", "ON_ERROR_STOP=1",
			"-U", databaseUser(getenv), "-d", databaseName(getenv),
			"-v", "api_env="+getenv("API_ENV"), "-f", "-")
		cmd := exec.CommandContext(ctx, runner.bin, line...)
		cmd.Stdin = bytes.NewReader(sql)
		combined, err := cmd.CombinedOutput()
		if err == nil {
			return nil
		}
		if message := strings.TrimSpace(string(combined)); message != "" {
			return fmt.Errorf("%s", message)
		}
		return err
	}
}

// liveEnsureBucket creates the configured bucket. It contacts the endpoint only
// after the configuration and the adapter validated, so an unconfigured store
// fails closed without touching the network.
func liveEnsureBucket(ctx context.Context, cfg config.Config) (string, error) {
	store, err := storage.NewS3(storage.Config{
		Endpoint:        cfg.S3Endpoint,
		Bucket:          cfg.S3Bucket,
		Region:          cfg.S3Region,
		AccessKeyID:     cfg.S3AccessKeyID,
		SecretAccessKey: cfg.S3SecretAccessKey,
	})
	if err != nil {
		return "", err
	}
	if err := store.EnsureBucket(ctx); err != nil {
		return "", err
	}
	return cfg.S3Bucket, nil
}

// databaseName and databaseUser follow the convention of the shell scripts in
// this repository (DB_NAME, DB_USER) and fall back to the compose settings and
// then to the development database, so a throwaway database is reached the same
// way the migration runner reaches it.
func databaseName(getenv func(string) string) string {
	if name := strings.TrimSpace(getenv("DB_NAME")); name != "" {
		return name
	}
	return nonEmpty(getenv("POSTGRES_DB"), defaultDatabase)
}

func databaseUser(getenv func(string) string) string {
	if user := strings.TrimSpace(getenv("DB_USER")); user != "" {
		return user
	}
	return nonEmpty(getenv("POSTGRES_USER"), defaultDatabaseUser)
}

func nonEmpty(value, fallback string) string {
	if trimmed := strings.TrimSpace(value); trimmed != "" {
		return trimmed
	}
	return fallback
}
