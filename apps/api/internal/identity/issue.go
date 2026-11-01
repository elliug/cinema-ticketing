// The issuing half of the login link: count the request against the address, mint and record
// one signed link, and only then mail it.
//
// This file is the domain only. It has no HTTP route and no process wiring — the transport
// that receives the request belongs to the task that follows — and it knows nothing about
// PostgreSQL or Redis: [AccessLinkStore], [LinkLimiter] and [Mailer] are the whole of what it
// needs from the world, so every rule here is provable with fakes.
//
// # The order is the contract
//
// The steps cannot be reordered without changing what a person can end up with:
//
//  1. Normalize the address again, and refuse one that normalizes to nothing. The caller
//     validates and normalizes its own input, and doing it twice is not redundancy: the
//     address is the account (S3), so the string this file counts, signs, records and mails
//     has to be the one spelling [NormalizeEmail] defines, and two callers that disagree about
//     case or padding would be two accounts for one person.
//  2. Count the request against the address, before anything else is spent (S6). A counter
//     that refuses and a counter that cannot be read both stop here and answer the same
//     error: an unreadable count is never permission, exactly as in the ticket resend's
//     [email.ResendLimiter].
//  3. Mint the nonce, sign the link, and write the row that names it — and only then send.
//     A link that was mailed but not recorded could never be spent: it would work until it
//     expired, as many times as anybody presented it, which is a key and not a step (S2).
//
// Counting before the mail is conservative in the attacker's favour, and deliberately: a
// failed send still costs one of the address's allowance, which is the cheaper mistake when
// the alternative is a burst that outruns a counter read after the fact.
//
// # What the caller must do with a refusal
//
// [Issuer.Issue] answers [ErrLinkRateLimited] for both a spent allowance and a counter that
// could not be read, and one plain error for a store or provider failure. The refusal exists
// for the operator, not for the person: the login screen answers a refused request exactly as
// it answers a sent one, because a different answer would tell a stranger that this address is
// being asked about. No code path in this file asks whether the address has an account —
// issuing never looks — so there is nothing here that could leak the answer even if the
// refusal were reported.
//
// # What is never written down
//
// Not the token, not the link and not the address. The address is a person, the token is a
// credential and the link is the credential in a URL, so the logger this file holds is used for
// the refusal and never for the request: the one diagnostic it records is that the counter
// failed, and the counter's own error is deliberately not logged either, because a Redis key in
// a message would carry the address into the log through the back door.
package identity

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"cinema.local/ticketing/apps/api/internal/venue"
)

// accessLinkKeyPrefix is the documented namespace of the per-address counter, one key per
// address inside the limiter's window. It is namespaced like the other counters this project
// keeps (`email:resend:`, `reservation:{…}`) so an operator reading the keyspace can tell which
// feature owns a key.
const accessLinkKeyPrefix = "identity:link:"

// accessLinkSubject is the one line the login email carries. It says "sign-in" and not
// "tickets" on purpose: this message and the purchase's message must not be mistaken for each
// other, because one lives for minutes and opens an account and the other lives for a week and
// opens a ticket.
const accessLinkSubject = "Your CinemaTicketing sign-in link"

// ErrLinkRateLimited reports that no link was issued because the address is inside its window,
// or because the counter could not be read at all. It is one error for both — see the file
// comment — and it never names the address or the remaining wait: the login surface answers it
// with the same words as a successful request.
var ErrLinkRateLimited = errors.New("identity: this address has asked for a sign-in link too often")

// LinkLimiter is the anti-relay bound as a boundary, so the issuer never knows whether it
// counts in Redis or in a test's map. It must be safe for concurrent use.
type LinkLimiter interface {
	// Allow counts one request for the key and reports whether it is inside the limit, with
	// the wait left in the window when it is not. An error means the count is unknown, and
	// the caller must refuse rather than treat it as allowed.
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// AccessLink is the row that makes a link spendable once: the nonce the signature names, the
// address it was mailed to and the instant it closes. It carries no token: the signature is
// what proves the link, and the row only has to remember that this nonce was issued and has
// not been used.
type AccessLink struct {
	// Nonce is the single-use identifier inside the signed payload. It is generated by the
	// caller and recorded, because it is part of what the signature covers.
	Nonce string
	// Email is the normalized address the link was addressed to, so consuming the link cannot
	// be turned into verifying a different address by editing a row.
	Email string
	// ExpiresAt is the instant the link stops opening, exactly as the signature spells it.
	ExpiresAt time.Time
}

// AccessLinkStore is the durable half of issuing: one row, written after the token is built
// and before the mail leaves. It is deliberately narrow, so storing a link cannot become a
// second place where its rules are written.
type AccessLinkStore interface {
	// Save records one issued link. It answers an error when the row was not written, and a
	// link whose row was not written must not be mailed.
	Save(ctx context.Context, link AccessLink) error
}

// Mail is one message this package asks a provider to send.
type Mail struct {
	// To is the normalized address the link was minted for.
	To string
	// Subject is a single line, never empty.
	Subject string
	// Body is plain text. No HTML: this message's only job is to carry a URL and a warning,
	// and plain text avoids a whole class of rendering differences in an inbox.
	Body string
}

// Mailer is the provider boundary: the project's existing sender today, an API tomorrow. It
// must be safe for concurrent use, because several people may ask for a link at once.
type Mailer interface {
	Send(ctx context.Context, mail Mail) error
}

// Issuer mints and mails one login link per request. It holds the signer that owns the key and
// the lifetime, the store that records the nonce, the counter that bounds the address, the
// provider boundary, the URL prefix the token is appended to, and the logger it uses for
// refusals only.
//
// It holds no clock and no lifetime of its own: the signer decides how long a link lives, and
// the expiry written to the row is read back out of the token that was just signed, so the row
// and the mail cannot disagree about the instant the link closes.
type Issuer struct {
	signer   *AccessSigner
	store    AccessLinkStore
	limiter  LinkLimiter
	mailer   Mailer
	linkBase string
	logger   *slog.Logger
}

// NewIssuer builds the issuing half and refuses a wiring that could not send anything, naming
// the missing piece instead of failing the first person who asks.
//
// linkBase is everything before the token, slash included — the caller's `https://host/in/`.
// Only its presence is checked here: the shape of a public URL is configuration's business,
// and a second opinion about it in this file would be a second place to keep it right. A nil
// logger falls back to slog.Default(), so an operator can never lose the refusal diagnostics by
// forgetting to wire one.
func NewIssuer(signer *AccessSigner, store AccessLinkStore, limiter LinkLimiter, mailer Mailer, linkBase string, logger *slog.Logger) (*Issuer, error) {
	if signer == nil {
		return nil, errors.New("identity: an issuer needs a signer, and it was given none")
	}
	if store == nil {
		return nil, errors.New("identity: an issuer needs a link store, and it was given none")
	}
	if limiter == nil {
		return nil, errors.New("identity: an issuer needs a rate limit, and it was given none")
	}
	if mailer == nil {
		return nil, errors.New("identity: an issuer needs a mailer, and it was given none")
	}
	base := strings.TrimSpace(linkBase)
	if base == "" {
		return nil, errors.New("identity: an issuer needs the URL prefix its links are built on, and it was given none")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Issuer{signer: signer, store: store, limiter: limiter, mailer: mailer, linkBase: base, logger: logger}, nil
}

// Issue mints one login link for one address and mails it.
//
// It answers nil when a link was recorded and sent, [ErrLinkRateLimited] when the address is
// inside its window or the counter could not be read, and the store's or the provider's own
// failure otherwise — wrapped, and never with the address or the token in the message.
//
// The caller is expected to answer the refusal exactly as it answers success: see the file
// comment. Nothing here looks up whether the address has an account, so the only fact the
// answer could carry is that this address is being asked about, and that is the fact the
// refusal exists to withhold.
func (i *Issuer) Issue(ctx context.Context, email string) error {
	if i == nil || i.signer == nil || i.store == nil || i.limiter == nil || i.mailer == nil {
		return errors.New("identity: an issuer needs a signer, a store, a limit and a mailer, and it was built without them")
	}

	// Normalized again, and refused before the counter: an empty address would share one
	// counter key with every other blank request, which is how one caller's allowance would pay
	// for another's — the same reason the ticket resend refuses an empty reservation id.
	address := NormalizeEmail(email)
	if address == "" {
		return fmt.Errorf("%w: the login email names no address", ErrUnsignable)
	}

	allowed, retryAfter, err := i.limiter.Allow(ctx, accessLinkKey(address))
	if err != nil {
		// Fail closed (S6): an unreadable counter is not permission. The underlying error is
		// neither wrapped nor logged, because a counter that names its key in its message would
		// put the address in the log. What the operator gets is which of the two refusals it was.
		i.logger.Warn("access link refused: the sign-in limit could not be read")
		return ErrLinkRateLimited
	}
	if !allowed {
		// The wait is the operator's to read, never the caller's: a Retry-After handed back to
		// the person would confirm that this address is being asked about.
		i.logger.Warn("access link refused: the address is inside its window", "retry_after", retryAfter)
		return ErrLinkRateLimited
	}

	// The nonce is the project's own UUID minter, the identifier every other row here uses, so
	// the nonce the signature names is shaped like the primary key it becomes.
	nonce := venue.NewID()
	now := time.Now().UTC()
	token, err := i.signer.Sign(address, nonce, now)
	if err != nil {
		return fmt.Errorf("identity: sign the access link: %w", err)
	}
	// The expiry is read back out of the token that was just signed instead of being computed
	// a second time as now+ttl: the row must end at the instant the signature names, and two
	// computations of one instant are two chances to disagree. It also proves the token this
	// package is about to mail is one this package can verify.
	claims, err := i.signer.Verify(token, now)
	if err != nil {
		return fmt.Errorf("identity: read back the access link that was just signed: %w", err)
	}

	if err := i.store.Save(ctx, AccessLink{
		Nonce: claims.Nonce, Email: claims.Email, ExpiresAt: claims.ExpiresAt,
	}); err != nil {
		// Nothing is mailed: a link whose row was not written could never be spent, and a URL
		// that keeps working until it expires is the opposite of what a login step is.
		return fmt.Errorf("identity: record the access link: %w", err)
	}

	// The URL is the prefix and the token, and nothing else: the token is URL-safe base64 and a
	// dot, so it needs no escaping and no query to survive a mail client.
	link := i.linkBase + token
	if err := i.mailer.Send(ctx, Mail{
		To:      claims.Email,
		Subject: accessLinkSubject,
		Body:    accessLinkBody(link, claims.ExpiresAt.Sub(now)),
	}); err != nil {
		return fmt.Errorf("identity: send the access link: %w", err)
	}
	return nil
}

// accessLinkKey is the counter key of one address. It exists so the naming is written once and
// a test can name the key it expects without repeating a format string.
func accessLinkKey(email string) string { return accessLinkKeyPrefix + email }

// accessLinkBody is the whole login message. It states what the link is, how long it lives and
// what it will not do, because the inbox is where a person reads a link before clicking it: an
// access link that is forwarded, archived or shared keeps working until its window closes, and
// the message is the only warning there is.
//
// The window is rounded to whole minutes and never reported as less than one: a lifetime of
// fifteen minutes truncated to the second would otherwise print as fourteen, which reads as a
// promise this deployment does not keep.
func accessLinkBody(link string, window time.Duration) string {
	minutes := int(window.Round(time.Minute) / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	return strings.Join([]string{
		"Here is your CinemaTicketing sign-in link:",
		"",
		link,
		"",
		fmt.Sprintf("It expires in %d minutes and it opens once, so it will not work a second time.", minutes),
		"",
		"If you did not ask to sign in, ignore this message: nothing has changed and nobody has",
		"signed in. Do not forward it, because anyone who has it can sign in as you.",
		"",
		"CinemaTicketing",
	}, "\n")
}
