// The deployed counter behind [LinkLimiter]: how many sign-in links one address may be sent
// inside one window (S6).
//
// # Why a counter at all
//
// Asking for a link is a public request that makes this deployment deliver mail to an address
// the caller names. Uncapped, that is a relay: anybody could post any mailbox and have the
// login email arrive, as often as they liked. The bound is per address and small, and it is
// checked before anything is signed or mailed, exactly as the ticket resend's counter is
// ([email.ResendLimiter]), because the cheaper mistake is to refuse one request too many.
//
// # Why it fails closed
//
// A counter that cannot be read is not permission. Redis being down must not turn the login
// route into an unthrottled mail sender, so a failed call answers an error here and the
// issuer above turns that into the same refusal a spent allowance answers — deliberately the
// same, because a different answer would tell a stranger that this address is being asked
// about (see [Issuer.Issue]).
//
// # What is never written down
//
// The key carries the address, because that is what is being counted, and nothing here logs a
// key, an error message from the server or the hash the login eventually writes. An operator
// reading the keyspace can see which addresses asked and how often; that is the point of the
// namespace, and it is the one place this fact lives.
package identity

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// The documented bound, and the window it is counted over.
const (
	// LinkLimit is how many sign-in links one address may ask for inside LinkWindow. Three
	// covers the ordinary case the feature exists for — "it did not arrive, send it again,
	// and once more to my other device" — and is small enough that a mailbox cannot be
	// flooded through this route.
	LinkLimit = 3
	// LinkWindow is the period the bound is counted over. It is also the lifetime of the
	// counter key, so an address that goes quiet for a quarter of an hour starts again with
	// a full allowance. It is minutes rather than hours because an access link itself lives
	// minutes (S2): a window outliving the link several times over would let one address be
	// mailed long after any of the links still opened anything.
	LinkWindow = 15 * time.Minute
)

// linkScript counts one request and answers what the window has left, in one server side
// execution. INCR and the EXPIRE that starts the window cannot interleave with another
// request, so a burst cannot read the same count twice and no counter can be left without a
// lifetime. The EXPIRE is inside `count == 1` so the window is started once and never
// restarted by a later request — a lifetime that moved forward on every increment would never
// expire while somebody kept asking, which is the opposite of a window. ARGV[1] is the window
// in seconds; the reply is {count, ttl}.
const linkScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('TTL', KEYS[1])
return {count, ttl}
`

// RedisLinkLimiter is the deployed counter: one key per address in the project's coordination
// layer. It is safe for concurrent use; all of its state is in Redis.
type RedisLinkLimiter struct {
	client redis.UniversalClient
	script *redis.Script
}

// compile-time proof that the deployed counter is the boundary the issuer is written against.
var _ LinkLimiter = (*RedisLinkLimiter)(nil)

// NewRedisLinkLimiter builds the counter on top of an existing Redis client, so the
// connection, TLS and pooling policy stay with the caller and no second pool is opened for a
// handful of increments a quarter of an hour.
func NewRedisLinkLimiter(client redis.UniversalClient) *RedisLinkLimiter {
	return &RedisLinkLimiter{client: client, script: redis.NewScript(linkScript)}
}

// Allow counts one request for the key its caller named and reports whether it fits in the
// window, with the wait the window has left when it does not.
//
// The key is the caller's, not this file's: [Issuer] builds it, so the address is counted in
// one namespaced spelling that is written once. This file writes no message that echoes the
// key; the Redis failure is wrapped only so a caller that wants the reason has it, and the one
// caller that exists ([Issuer.Issue]) deliberately does not log it — a Redis error can name
// the key it was given, which would carry an address into a log through the back door.
//
// It answers an error whenever Redis does, including an unreadable reply: the issuer above
// turns that into a refusal, which is the fail-closed half of the bound.
func (l *RedisLinkLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	counted := strings.TrimSpace(key)
	if counted == "" {
		// Every blank request would share one counter, so one caller's allowance would pay
		// for another's. Refusing is also what the caller does with the error.
		return false, 0, fmt.Errorf("%w: a request to count names no identifier", ErrLinkRateLimited)
	}

	reply, err := l.script.Run(ctx, l.client, []string{counted},
		strconv.FormatInt(int64(LinkWindow/time.Second), 10)).Int64Slice()
	if err != nil {
		// Wrapped so a caller that wants the reason has it, and never logged by the caller
		// that exists: a Redis error can name the key, and the key carries the address.
		return false, 0, fmt.Errorf("identity: the sign-in limit could not be counted: %w", err)
	}
	if len(reply) != 2 {
		return false, 0, fmt.Errorf("identity: the sign-in limit answered %d value(s), want a count and a window", len(reply))
	}

	// A counter without a lifetime would lock an address out forever, so the documented
	// window is used rather than answering "wait zero seconds".
	retryAfter := time.Duration(reply[1]) * time.Second
	if retryAfter <= 0 || retryAfter > LinkWindow {
		retryAfter = LinkWindow
	}
	return reply[0] <= LinkLimit, retryAfter, nil
}
