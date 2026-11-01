// Behaviour tests for the Redis counter that bounds how often one address may be asked
// about (T2, S6).
//
// The bound is the anti-relay half of the login route: without it, anybody could post any
// mailbox and make this deployment deliver link after link into it. What is pinned here,
// against miniredis and therefore against a real server's own Lua execution: the fourth
// request inside the window is refused with the wait the window has left; the window is
// started once, by the first increment, and a second increment inside it does not extend it;
// each key is counted on its own, so one address's allowance cannot pay for another's; and a
// Redis that cannot answer is an error, never permission — failing open here would be
// exactly the abuse the counter exists to stop.
package identity_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// identityLimiterKey is one address's key, spelled the way the issuer spells it: the limiter
// counts what its caller names and invents no namespace of its own.
const identityLimiterKey = "identity:link:ana@example.test"

// otherIdentityLimiterKey is a second address, so a case can prove one allowance is not
// another's.
const otherIdentityLimiterKey = "identity:link:bea@example.test"

// newRedisLinkLimiter starts a miniredis and the counter over it. maxRetries is lowered for
// the unreachable-server case so one failed call answers instead of spending the test on
// backoff.
func newRedisLinkLimiter(t *testing.T, maxRetries int) (*identity.RedisLinkLimiter, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: maxRetries})
	t.Cleanup(func() { _ = client.Close() })
	return identity.NewRedisLinkLimiter(client), server
}

// T2 (S6): three requests per address per window, and the fourth is refused with a positive
// wait inside the window. The counter is one key per address and exists only while its
// window runs.
func TestRedisLinkLimiterRefusesTheFourthRequestInTheWindow(t *testing.T) {
	limiter, server := newRedisLinkLimiter(t, 0)
	ctx := context.Background()

	for attempt := 1; attempt <= identity.LinkLimit; attempt++ {
		allowed, _, err := limiter.Allow(ctx, identityLimiterKey)
		if err != nil {
			t.Fatalf("Allow() attempt %d = unexpected error %v", attempt, err)
		}
		if !allowed {
			t.Fatalf("Allow() attempt %d was refused inside the limit of %d", attempt, identity.LinkLimit)
		}
	}

	if !server.Exists(identityLimiterKey) {
		t.Fatalf("no key %q after %d requests; the counters are %v",
			identityLimiterKey, identity.LinkLimit, server.Keys())
	}

	allowed, retryAfter, err := limiter.Allow(ctx, identityLimiterKey)
	if err != nil {
		t.Fatalf("Allow() over the limit = unexpected error %v", err)
	}
	if allowed {
		t.Fatalf("Allow() allowed request %d with a limit of %d", identity.LinkLimit+1, identity.LinkLimit)
	}
	if retryAfter <= 0 || retryAfter > identity.LinkWindow {
		t.Fatalf("retryAfter = %s, want a positive wait inside the %s window", retryAfter, identity.LinkWindow)
	}

	// A second address has its own allowance: one person's requests cannot spend another's.
	allowed, _, err = limiter.Allow(ctx, otherIdentityLimiterKey)
	if err != nil {
		t.Fatalf("Allow() of another address = unexpected error %v", err)
	}
	if !allowed {
		t.Fatal("one address's allowance was spent by another's requests")
	}
}

// T2 (S6): the window is the key's own lifetime, set by the first increment and not moved by
// the ones after it. A counter whose lifetime restarted on every request would never expire
// while somebody kept asking, which is the opposite of a window.
func TestRedisLinkLimiterStartsTheWindowOnceOnTheFirstIncrement(t *testing.T) {
	limiter, server := newRedisLinkLimiter(t, 0)
	ctx := context.Background()

	allowed, retryAfter, err := limiter.Allow(ctx, identityLimiterKey)
	if err != nil || !allowed {
		t.Fatalf("Allow() first request = (%v, %s, %v), want an allowance", allowed, retryAfter, err)
	}
	if ttl := server.TTL(identityLimiterKey); ttl <= 0 || ttl > identity.LinkWindow {
		t.Fatalf("TTL after the first increment = %s, want the %s window", ttl, identity.LinkWindow)
	}

	// Five minutes into the window, a second request must not restart it.
	server.FastForward(5 * time.Minute)
	remaining := server.TTL(identityLimiterKey)
	allowed, retryAfter, err = limiter.Allow(ctx, identityLimiterKey)
	if err != nil || !allowed {
		t.Fatalf("Allow() second request = (%v, %s, %v), want an allowance", allowed, retryAfter, err)
	}
	after := server.TTL(identityLimiterKey)
	if after > remaining {
		t.Fatalf("TTL grew from %s to %s on a later increment: the window was restarted", remaining, after)
	}
	if after != remaining {
		t.Fatalf("TTL = %s after a second increment, want the unchanged %s", after, remaining)
	}
	// And the wait the counter reports is the window's, not the configured lifetime.
	if retryAfter > remaining {
		t.Fatalf("retryAfter = %s, want at most the %s left in the window", retryAfter, remaining)
	}

	// Once the window closes the key is gone and the address starts again with a full
	// allowance; the count is a window, not a lifetime ban.
	server.FastForward(identity.LinkWindow)
	if server.Exists(identityLimiterKey) {
		t.Fatalf("the counter %q survived its own window", identityLimiterKey)
	}
	allowed, _, err = limiter.Allow(ctx, identityLimiterKey)
	if err != nil || !allowed {
		t.Fatalf("Allow() after the window = (%v, %v), want a fresh allowance", allowed, err)
	}
}

// T2 (S6): an unreachable Redis is an error and never permission. The issuer above turns it
// into the same refusal a spent allowance answers, so the login route fails closed.
func TestRedisLinkLimiterRefusesWhenRedisCannotAnswer(t *testing.T) {
	// No retries: an unreachable server answers once, which is what the route sees.
	limiter, server := newRedisLinkLimiter(t, -1)
	server.Close()

	allowed, _, err := limiter.Allow(context.Background(), identityLimiterKey)
	if err == nil {
		t.Fatal("Allow() reported a decision from an unreachable Redis")
	}
	if allowed {
		t.Fatal("Allow() allowed a request although the counter could not be read")
	}
}

// T2 (S6): a request with no key names no address to count, and every blank request would
// share one counter — so one caller's allowance would pay for another's. It is refused, and
// nothing is written.
func TestRedisLinkLimiterRefusesARequestWithNoKey(t *testing.T) {
	limiter, server := newRedisLinkLimiter(t, 0)

	for _, key := range []string{"", " ", "\t\n"} {
		allowed, _, err := limiter.Allow(context.Background(), key)
		if err == nil {
			t.Fatalf("Allow(%q) reported a decision", key)
		}
		if allowed {
			t.Fatalf("Allow(%q) allowed a request that names no address", key)
		}
		if !strings.Contains(err.Error(), "identifier") {
			t.Fatalf("Allow(%q) error = %q, want it to say the request names no identifier", key, err)
		}
	}
	if keys := server.Keys(); len(keys) != 0 {
		t.Fatalf("a blank key wrote %v", keys)
	}
}
