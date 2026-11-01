// Behaviour tests for the ticket page's "send it to this address" action and for the
// per-reservation limit behind it (T5, S2, S3, S5).
//
// The action is what would turn a ticket page into a mail relay if it were unbounded:
// anybody holding a ticket identifier could post any mailbox to it, and the message carries
// a link that opens the tickets. So what is pinned here is not "does it call the sender" —
// it is the order of the counter against the send, the refusal when the counter cannot be
// read, and that the message is the consumer's own, because two spellings of the same email
// is a bug a buyer notices and nobody else does.
package email_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"cinema.local/ticketing/apps/api/internal/email"
)

const (
	// resendReservation is the order a resend is asked for in the fixtures.
	resendReservation = "00000000-0000-0000-0000-00000000e5a1"
	// otherResendReservation is a second order, used to prove the counter is per
	// reservation and not a global allowance.
	otherResendReservation = "00000000-0000-0000-0000-00000000e5a2"
	// resendKeyPrefix is the documented Redis namespace of the anti-relay counter.
	resendKeyPrefix = "email:resend:"
)

// resendLimiter is the anti-relay bound in memory. It counts per reservation the way the
// Redis one does, and it appends to the shared sequence so one test can prove the counter is
// spent *before* the message leaves: a burst that outran the counter would be the whole
// abuse the limit exists to stop.
type resendLimiter struct {
	limit      int
	retryAfter time.Duration
	err        error

	asked    []string
	counts   map[string]int
	sequence *[]string
}

func (l *resendLimiter) Allow(_ context.Context, reservationID string) (bool, time.Duration, error) {
	l.asked = append(l.asked, reservationID)
	if l.sequence != nil {
		*l.sequence = append(*l.sequence, "allow")
	}
	if l.err != nil {
		return false, 0, l.err
	}
	if l.counts == nil {
		l.counts = make(map[string]int)
	}
	l.counts[reservationID]++
	return l.counts[reservationID] <= l.limit, l.retryAfter, nil
}

// resendSender records the messages it was handed, in order, so a test can compare them and
// can see where the send sits relative to the counter.
type resendSender struct {
	messages []email.Message
	failWith error
	sequence *[]string
}

func (s *resendSender) Send(_ context.Context, message email.Message) error {
	if s.sequence != nil {
		*s.sequence = append(*s.sequence, "send")
	}
	if s.failWith != nil {
		return s.failWith
	}
	s.messages = append(s.messages, message)
	return nil
}

// resendLink is the link a resend test mails; the body is what carries it.
func resendLink(string) (string, error) { return testLink, nil }

// newResender builds the action under test over the fakes, and fails the test when the
// wiring is refused.
func newResender(t *testing.T, sender email.Sender, limiter email.ResendLimiter, link email.LinkBuilder) *email.Resender {
	t.Helper()
	resender, err := email.NewResender(sender, link, limiter)
	if err != nil {
		t.Fatalf("NewResender() = unexpected error %v", err)
	}
	return resender
}

// T5 (S2, S3): a resend is the purchase email. The body, the subject and the signed link
// are the consumer's own — compared here against what the worker actually sends — so a
// buyer forwarding the tickets to a colleague gets the same mail, not a second version of
// it that drifts on the next edit.
func TestResendSendsTheSameMessageTheConsumerSends(t *testing.T) {
	store := &fakeStore{
		rows:       []email.Row{row("outbox-1", resendReservation)},
		recipients: map[string]string{resendReservation: "buyer@example.com"},
	}
	consumer := &fakeSender{}
	if err := newWorker(t, store, consumer).Pass(context.Background()); err != nil {
		t.Fatalf("Pass() = unexpected error %v", err)
	}
	if len(consumer.messages) != 1 {
		t.Fatalf("the consumer sent %d message(s), want one", len(consumer.messages))
	}

	// The very same link builder the worker is wired with, so the only difference between
	// the two messages can be the recipient.
	sender := &resendSender{}
	resender := newResender(t, sender, &resendLimiter{limit: email.ResendLimit}, resendLink)
	if err := resender.Resend(context.Background(), resendReservation, "colleague@example.com"); err != nil {
		t.Fatalf("Resend() = unexpected error %v", err)
	}
	if len(sender.messages) != 1 {
		t.Fatalf("the resend sent %d message(s), want exactly one", len(sender.messages))
	}

	sent := sender.messages[0]
	consumed := consumer.messages[0]
	if sent.Subject != consumed.Subject {
		t.Fatalf("subject = %q, want the consumer's %q", sent.Subject, consumed.Subject)
	}
	if sent.Body != consumed.Body {
		t.Fatalf("body = %q, want the consumer's %q", sent.Body, consumed.Body)
	}
	if !strings.Contains(sent.Body, testLink) {
		t.Fatalf("body = %q, want it to carry the signed link %q", sent.Body, testLink)
	}
	// The address is the one typed now, never the one stored with the order.
	if sent.To != "colleague@example.com" {
		t.Fatalf("To = %q, want the address this request named", sent.To)
	}
}

// T5 (S5): a provider that fails is reported as a failure, and the counter was already
// spent. The order matters: counting first means a provider that is down cannot be used as
// a free relay, and it means a caller cannot spend the reservation's allowance twice by
// making the first attempt fail.
func TestResendReportsAFailedSendAsAFailure(t *testing.T) {
	var sequence []string
	limiter := &resendLimiter{limit: email.ResendLimit, sequence: &sequence}
	sender := &resendSender{failWith: errors.New("connection refused"), sequence: &sequence}
	resender := newResender(t, sender, limiter, resendLink)

	err := resender.Resend(context.Background(), resendReservation, "colleague@example.com")
	if err == nil {
		t.Fatal("Resend() reported success for a provider that failed")
	}
	if !errors.Is(err, email.ErrUnavailable) {
		t.Fatalf("Resend() error = %v, want it to wrap ErrUnavailable", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("the failing provider recorded %d message(s), want none", len(sender.messages))
	}
	// The counter was spent before the send, not after it.
	if got := sequence; len(got) != 2 || got[0] != "allow" || got[1] != "send" {
		t.Fatalf("call order = %v, want the counter before the send", got)
	}
	if got := limiter.counts[resendReservation]; got != 1 {
		t.Fatalf("counter = %d after one failed send, want 1: the attempt was made", got)
	}
}

// T5 (S5): the limit holds at the fourth send and nothing leaves on the refusal. Three
// messages an hour per order is what keeps this route from being a relay; a refusal that
// still sent would be no limit at all.
func TestResendRefusesOverTheLimitWithoutSending(t *testing.T) {
	limiter := &resendLimiter{limit: email.ResendLimit, retryAfter: 42 * time.Minute}
	sender := &resendSender{}
	resender := newResender(t, sender, limiter, resendLink)

	for attempt := 1; attempt <= email.ResendLimit; attempt++ {
		if err := resender.Resend(context.Background(), resendReservation, "colleague@example.com"); err != nil {
			t.Fatalf("resend %d of %d = unexpected error %v", attempt, email.ResendLimit, err)
		}
	}

	err := resender.Resend(context.Background(), resendReservation, "colleague@example.com")
	if err == nil {
		t.Fatal("Resend() allowed one send over the limit")
	}
	var limited *email.RateLimitedError
	if !errors.As(err, &limited) {
		t.Fatalf("Resend() error = %v, want a *RateLimitedError", err)
	}
	if !errors.Is(err, email.ErrRateLimited) {
		t.Fatalf("Resend() error = %v, want it to wrap ErrRateLimited", err)
	}
	// The wait is the caller's only way to say something useful, so it is carried, not
	// invented: it is what the counter has left of its window.
	if limited.RetryAfter != 42*time.Minute {
		t.Fatalf("RetryAfter = %s, want the counter's remaining %s", limited.RetryAfter, 42*time.Minute)
	}
	if len(sender.messages) != email.ResendLimit {
		t.Fatalf("the provider was handed %d message(s), want exactly the %d allowed",
			len(sender.messages), email.ResendLimit)
	}

	// Another order has its own allowance: the limit bounds one reservation, not the
	// deployment.
	if err := resender.Resend(context.Background(), otherResendReservation, "colleague@example.com"); err != nil {
		t.Fatalf("Resend() of another reservation = unexpected error %v", err)
	}
}

// T5 (S5): a counter that cannot be read refuses the send. This is the fail-closed half of
// the limit: Redis being down must not turn the route into an unthrottled mail sender, so
// an outage costs a 503 instead of a free relay.
func TestResendRefusesWhenTheLimitCannotBeChecked(t *testing.T) {
	limiter := &resendLimiter{limit: email.ResendLimit, err: errors.New("redis is down")}
	sender := &resendSender{}
	resender := newResender(t, sender, limiter, resendLink)

	err := resender.Resend(context.Background(), resendReservation, "colleague@example.com")
	if err == nil {
		t.Fatal("Resend() sent although the limit could not be checked")
	}
	if !errors.Is(err, email.ErrUnavailable) {
		t.Fatalf("Resend() error = %v, want it to wrap ErrUnavailable", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("the provider was handed %d message(s), want none", len(sender.messages))
	}
}

// T5 (S2): the action refuses a request with no recipient at all before it spends the
// order's allowance. The route validates the address first, so this is the second line of
// defence rather than the ordinary path — and it is the one that keeps a defect from
// costing a buyer one of three sends an hour.
func TestResendRefusesWithoutARecipient(t *testing.T) {
	for _, address := range []string{"", "   "} {
		limiter := &resendLimiter{limit: email.ResendLimit}
		sender := &resendSender{}
		resender := newResender(t, sender, limiter, resendLink)

		err := resender.Resend(context.Background(), resendReservation, address)
		if !errors.Is(err, email.ErrInvalidRecipient) {
			t.Fatalf("Resend(%q) error = %v, want it to wrap ErrInvalidRecipient", address, err)
		}
		if len(sender.messages) != 0 {
			t.Fatalf("Resend(%q) sent %d message(s), want none", address, len(sender.messages))
		}
		if len(limiter.asked) != 0 {
			t.Fatalf("Resend(%q) spent the counter on a request with no recipient", address)
		}
	}
}

// T5: a resend built without the half it needs is refused at construction, so a wiring
// mistake is one startup error instead of a route that fails every request.
func TestNewResenderRefusesAnImpossibleWiring(t *testing.T) {
	sender := &resendSender{}
	limiter := &resendLimiter{limit: email.ResendLimit}

	if _, err := email.NewResender(nil, resendLink, limiter); err == nil {
		t.Fatal("NewResender(nil sender) was accepted")
	}
	if _, err := email.NewResender(sender, nil, limiter); err == nil {
		t.Fatal("NewResender(nil link builder) was accepted")
	}
	if _, err := email.NewResender(sender, resendLink, nil); err == nil {
		t.Fatal("NewResender(nil limiter) was accepted")
	}
}

// T5 (S5): the Redis counter is one key per reservation, its window is the key's TTL, and
// the fourth send inside that window is refused with the wait it has left. The in-process
// fake is a fake and not a server: nothing here needs a running Redis, which is why the
// abuse bound is testable at all.
func TestRedisResendLimiterCountsPerReservation(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	limiter := email.NewRedisResendLimiter(client)
	ctx := context.Background()

	for attempt := 1; attempt <= email.ResendLimit; attempt++ {
		allowed, _, err := limiter.Allow(ctx, resendReservation)
		if err != nil {
			t.Fatalf("Allow() attempt %d = unexpected error %v", attempt, err)
		}
		if !allowed {
			t.Fatalf("Allow() attempt %d was refused inside the limit of %d", attempt, email.ResendLimit)
		}
	}

	// The counter is one namespaced key, and its window is the key's own lifetime, so a
	// reservation that goes quiet for an hour starts again.
	key := resendKeyPrefix + resendReservation
	if !server.Exists(key) {
		t.Fatalf("no key %q after %d sends; keys are %v", key, email.ResendLimit, server.Keys())
	}
	if ttl := server.TTL(key); ttl <= 0 || ttl > email.ResendWindow {
		t.Fatalf("TTL(%q) = %s, want a positive window of at most %s", key, ttl, email.ResendWindow)
	}

	allowed, retryAfter, err := limiter.Allow(ctx, resendReservation)
	if err != nil {
		t.Fatalf("Allow() over the limit = unexpected error %v", err)
	}
	if allowed {
		t.Fatalf("Allow() allowed send %d with a limit of %d", email.ResendLimit+1, email.ResendLimit)
	}
	if retryAfter <= 0 || retryAfter > email.ResendWindow {
		t.Fatalf("retryAfter = %s, want a positive wait inside the %s window", retryAfter, email.ResendWindow)
	}

	// A different order has its own counter.
	allowed, _, err = limiter.Allow(ctx, otherResendReservation)
	if err != nil {
		t.Fatalf("Allow() of another reservation = unexpected error %v", err)
	}
	if !allowed {
		t.Fatal("one reservation's allowance was spent by another's sends")
	}
}

// T5 (S5): when Redis cannot answer, the counter answers "not allowed" with an error, and
// the action above it turns that into a refusal. Failing open here would be the abuse the
// limit exists to stop, so the closed door is pinned.
func TestRedisResendLimiterRefusesWhenRedisCannotAnswer(t *testing.T) {
	server := miniredis.RunT(t)
	// No retries, so an unreachable Redis answers once instead of spending the test's time
	// on backoff: the action above refuses on the first error either way.
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() { _ = client.Close() })
	limiter := email.NewRedisResendLimiter(client)

	// A closed fake is a Redis that cannot be reached: no server, no listening port.
	server.Close()

	allowed, _, err := limiter.Allow(context.Background(), resendReservation)
	if err == nil {
		t.Fatal("Allow() reported a decision from an unreachable Redis")
	}
	if allowed {
		t.Fatal("Allow() allowed a send although the counter could not be read")
	}
}
