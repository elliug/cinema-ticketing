// The resend action: the ticket email of one order, mailed to an address typed on a ticket
// page at that moment, bounded per reservation so the API cannot be used as a relay.
//
// # Why it lives in this package
//
// Because the message is not a second message. The subject and the text are the consumer's
// own ([Subject] and the same composer the worker uses), and the link is the incoming
// [LinkBuilder], so a resend is literally the purchase email sent again. Two spellings of
// "here are your tickets" would be a bug a buyer notices when they compare two inboxes and
// nobody else ever does.
//
// # Nothing is stored
//
// The address arrives with the request and dies with it. It is not written to the
// reservation, not recorded as a second address of the order, not put in the outbox and not
// logged. The product decision is that the panel offers a forward, not a second recipient.
//
// # Why the counter is spent before the message leaves
//
// The limit is the anti-relay bound: three sends per reservation per hour. Counting after a
// successful send would let a burst outrun it — N concurrent requests all read the same
// count — and would also make a provider failure free to retry, so an attacker does not need
// the mail to succeed to keep the route busy. Counting first is conservative in the buyer's
// favour of the abuse: a failed send costs one of the three, and that is the cheaper
// mistake.
//
// A counter that cannot be read refuses the send for the same reason. Redis being down must
// not turn this route into an unthrottled mail sender, so the failure is a 503 and never a
// polite "allowed".
package email

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// The documented anti-relay bound: sends per reservation per window.
const (
	// ResendLimit is how many times one reservation may mail its ticket email inside
	// ResendWindow. Three is enough for the ordinary case the feature exists for — "send it
	// to my partner, and later to my colleague" — and small enough that a mailbox cannot be
	// flooded through one order.
	ResendLimit = 3
	// ResendWindow is the period the bound is counted over. It is also the lifetime of the
	// counter key, so a reservation that goes quiet starts again with a full allowance.
	ResendWindow = time.Hour
	// resendKeyPrefix is the documented Redis namespace of the counter: one key per
	// reservation, holding the number of sends inside the current window. It is namespaced
	// like the hold keys (`reservation:{event:<id>}:seat:<n>`) so an operator reading the
	// keyspace can tell which feature owns a key.
	resendKeyPrefix = "email:resend:"
)

// Errors the resend action reports.
var (
	// ErrRateLimited reports that the reservation has spent its allowance for the window.
	// A caller learns how long to wait from the [RateLimitedError] it is wrapped in.
	ErrRateLimited = errors.New("email: this reservation has been emailed too often")
	// ErrInvalidRecipient reports a resend with no address to mail. It is caller input and
	// not an outage: the route validates the address before it gets here, and this is the
	// second line of defence that keeps a defect from spending the order's allowance.
	ErrInvalidRecipient = errors.New("email: a resend needs a recipient address")
)

// RateLimitedError is the refusal of a resend that is over the limit. It carries the wait
// the counter has left, because that is the only thing the caller can tell the buyer.
type RateLimitedError struct {
	// RetryAfter is the time left in the current window. It is at least a second and never
	// longer than ResendWindow.
	RetryAfter time.Duration
}

func (e *RateLimitedError) Error() string {
	return fmt.Sprintf("%v: retry in %s", ErrRateLimited, e.RetryAfter.Round(time.Second))
}

// Unwrap makes errors.Is(err, ErrRateLimited) answer for the typed refusal.
func (e *RateLimitedError) Unwrap() error { return ErrRateLimited }

// ResendLimiter is the anti-relay bound as a boundary, so the action never knows whether it
// counts in Redis or in a test's map. It must be safe for concurrent use.
type ResendLimiter interface {
	// Allow counts one send of the reservation and reports whether it is inside the limit,
	// with the wait left in the window when it is not. An error means the count is unknown,
	// and the caller must refuse the send rather than count it as allowed.
	Allow(ctx context.Context, reservationID string) (allowed bool, retryAfter time.Duration, err error)
}

// resendScript counts one send and answers what the window has left, in one server side
// execution: INCR and the EXPIRE that starts the window cannot interleave with another
// request, so no burst can read the same count twice and no counter can be left without a
// lifetime. ARGV[1] is the window in seconds; the reply is {count, ttl}.
const resendScript = `
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('EXPIRE', KEYS[1], ARGV[1])
end
local ttl = redis.call('TTL', KEYS[1])
return {count, ttl}
`

// RedisResendLimiter is the deployed counter: one key per reservation in the project's
// coordination layer. It is safe for concurrent use; all of its state is in Redis.
type RedisResendLimiter struct {
	client redis.UniversalClient
	script *redis.Script
}

// NewRedisResendLimiter builds the counter on top of an existing Redis client, so the
// connection, TLS and pooling policy stay with the caller and no second pool is opened for
// three increments an hour.
func NewRedisResendLimiter(client redis.UniversalClient) *RedisResendLimiter {
	return &RedisResendLimiter{client: client, script: redis.NewScript(resendScript)}
}

// Allow counts one send of the reservation and reports whether it fits in the window.
//
// It answers an error whenever Redis does, including an unreadable reply: the action above
// turns that into a refusal, which is the fail-closed half of the bound.
func (l *RedisResendLimiter) Allow(ctx context.Context, reservationID string) (bool, time.Duration, error) {
	reservation := strings.TrimSpace(reservationID)
	if reservation == "" {
		// The key would be shared by every empty identifier, which would make one
		// reservation's allowance pay for another's. Refusing is also what the caller does
		// with the error.
		return false, 0, fmt.Errorf("%w: a resend needs a reservation identifier", ErrInvalidRecipient)
	}

	reply, err := l.script.Run(ctx, l.client, []string{resendKey(reservation)},
		strconv.FormatInt(int64(ResendWindow/time.Second), 10)).Int64Slice()
	if err != nil {
		return false, 0, fmt.Errorf("email: count the resends of reservation %q: %w", reservation, err)
	}
	if len(reply) != 2 {
		return false, 0, fmt.Errorf("email: the resend counter answered %d value(s), want a count and a window", len(reply))
	}

	// A counter without a lifetime would lock a reservation forever, so the documented
	// window is used rather than answering "wait zero seconds".
	retryAfter := time.Duration(reply[1]) * time.Second
	if retryAfter <= 0 || retryAfter > ResendWindow {
		retryAfter = ResendWindow
	}
	return reply[0] <= ResendLimit, retryAfter, nil
}

// resendKey is the counter key of one reservation. It exists so the naming is written once
// and a test can name the key it expects without repeating a format string.
func resendKey(reservationID string) string { return resendKeyPrefix + reservationID }

// Resender mails the ticket email of one reservation to an address typed at that moment.
type Resender struct {
	sender  Sender
	link    LinkBuilder
	limiter ResendLimiter
}

// NewResender builds the action and refuses a wiring that could not send anything, naming
// the missing piece instead of failing the first request.
func NewResender(sender Sender, link LinkBuilder, limiter ResendLimiter) (*Resender, error) {
	if sender == nil {
		return nil, fmt.Errorf("%w: no mail provider is wired", ErrNotConfigured)
	}
	if link == nil {
		return nil, fmt.Errorf("%w: no ticket link builder is wired", ErrNotConfigured)
	}
	if limiter == nil {
		return nil, fmt.Errorf("%w: no resend limit is wired", ErrNotConfigured)
	}
	return &Resender{sender: sender, link: link, limiter: limiter}, nil
}

// Resend mails the reservation's ticket email to to. It stores nothing: the address is used
// for this message and forgotten.
//
// The steps are ordered because each one decides what the next may do:
//
//  1. Refuse a request with no recipient, before the counter is spent.
//  2. Count the send in the limit. A counter that cannot answer refuses here, and so does a
//     reservation that has spent its allowance: no provider is called for either.
//  3. Build the signed link, so a link that cannot be signed costs a provider call.
//  4. Send. A provider failure is an [ErrUnavailable], never a success.
func (r *Resender) Resend(ctx context.Context, reservationID, to string) error {
	recipient := strings.TrimSpace(to)
	if recipient == "" {
		return fmt.Errorf("%w: mail it to an address", ErrInvalidRecipient)
	}

	allowed, retryAfter, err := r.limiter.Allow(ctx, reservationID)
	if err != nil {
		return fmt.Errorf("%w: the resend limit cannot be checked: %v", ErrUnavailable, err)
	}
	if !allowed {
		return &RateLimitedError{RetryAfter: retryAfter}
	}

	link, err := r.link(reservationID)
	if err != nil {
		return fmt.Errorf("%w: build the ticket link: %v", ErrUnavailable, err)
	}

	if err := r.sender.Send(ctx, Message{
		To:      recipient,
		Subject: Subject,
		Body:    body(link),
	}); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}
