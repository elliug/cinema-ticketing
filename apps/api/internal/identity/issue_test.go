// Behaviour tests for issuing a login link (T2, S2, S3, S6).
//
// Issuing is three acts in one direction: count the request against the address, mint and
// record a link the signature names, and only then mail it. What is pinned here, with fakes
// and no database, no clock and no provider: the row saved carries the nonce and the expiry
// that are inside the token the mail carries, so the row cannot describe a different link
// from the one that was sent; the address is normalized again before it is counted or
// signed, so two spellings of one mailbox are one account; a counter that refuses and a
// counter that cannot be read both send nothing and answer one error, because an unreadable
// count is not permission and the person must not learn whether the address exists or was
// throttled; a store that could not record the link mails nothing, since a link nobody
// recorded could never be spent; the message is its own text, naming the minutes, the single
// use and the safety of ignoring it; and the address, the token and the link never reach the
// log.
package identity_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/identity"
)

// issuerLinkBase is the prefix the caller passes: everything before the token, slash
// included. It is not a URL this test opens; it only has to appear in front of the token.
const issuerLinkBase = "https://cinema.test/in/"

// The fixture address and the spelling a person typed of it. The typed one is deliberately
// different case and padded, so a case can prove the issuer normalizes again instead of
// trusting its caller.
const (
	issuerTypedEmail        = "  Ana@Example.TEST  "
	issuerNormalizedEmail   = "ana@example.test"
	issuerRateLimitKeyValue = "identity:link:" + issuerNormalizedEmail
)

// fakeAccessLinkStore records every row the issuer asks to save, and can refuse.
type fakeAccessLinkStore struct {
	saved []identity.AccessLink
	err   error
}

// compile-time proof that the fake is the contract the issuer is written against.
var _ identity.AccessLinkStore = (*fakeAccessLinkStore)(nil)

func (f *fakeAccessLinkStore) Save(_ context.Context, link identity.AccessLink) error {
	f.saved = append(f.saved, link)
	return f.err
}

// fakeLinkLimiter is the counter boundary: it records the keys it was asked about and
// answers what the case told it to, including "cannot read the count".
type fakeLinkLimiter struct {
	allowed    bool
	retryAfter time.Duration
	err        error
	keys       []string
}

var _ identity.LinkLimiter = (*fakeLinkLimiter)(nil)

func (f *fakeLinkLimiter) Allow(_ context.Context, key string) (bool, time.Duration, error) {
	f.keys = append(f.keys, key)
	return f.allowed, f.retryAfter, f.err
}

// fakeMailer records the messages that were handed to a provider.
type fakeMailer struct {
	sent []identity.Mail
	err  error
}

var _ identity.Mailer = (*fakeMailer)(nil)

func (f *fakeMailer) Send(_ context.Context, mail identity.Mail) error {
	f.sent = append(f.sent, mail)
	return f.err
}

// issueFixture is one issuer over fakes, plus the log it writes to so a case can assert
// what an operator would and would not see.
type issueFixture struct {
	issuer  *identity.Issuer
	signer  *identity.AccessSigner
	store   *fakeAccessLinkStore
	limiter *fakeLinkLimiter
	mailer  *fakeMailer
	logs    *bytes.Buffer
}

// newIssueFixture builds the issuer every case starts from: a real signer with the
// documented fifteen minute lifetime, an allowing counter and a store that records.
func newIssueFixture(t *testing.T) *issueFixture {
	t.Helper()
	f := &issueFixture{
		signer:  mustAccessSigner(t, accessTestKey, accessTTL),
		store:   &fakeAccessLinkStore{},
		limiter: &fakeLinkLimiter{allowed: true},
		mailer:  &fakeMailer{},
		logs:    &bytes.Buffer{},
	}
	logger := slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	issuer, err := identity.NewIssuer(f.signer, f.store, f.limiter, f.mailer, issuerLinkBase, logger)
	if err != nil {
		t.Fatalf("NewIssuer() = unexpected error %v", err)
	}
	f.issuer = issuer
	return f
}

// mailedLink answers the URL the message carries, which is the only way the link leaves this
// package: the token is never a return value.
func mailedLink(t *testing.T, mail identity.Mail) string {
	t.Helper()
	for _, line := range strings.Split(mail.Body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), issuerLinkBase) {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("the mail carries no line beginning with %q, so it carries no link", issuerLinkBase)
	return ""
}

// mailedToken answers the token the mailed URL carries.
func mailedToken(t *testing.T, mail identity.Mail) string {
	t.Helper()
	link := mailedLink(t, mail)
	token := strings.TrimPrefix(link, issuerLinkBase)
	if token == "" || token == link {
		t.Fatalf("the mailed URL %q carries no token after %q", link, issuerLinkBase)
	}
	return token
}

// onlyMail answers the one message the issuer sent.
func onlyMail(t *testing.T, mailer *fakeMailer) identity.Mail {
	t.Helper()
	if len(mailer.sent) != 1 {
		t.Fatalf("the issuer sent %d messages, want exactly 1", len(mailer.sent))
	}
	return mailer.sent[0]
}

// T2 (S2, S3): the row is written before the mail, and it describes exactly the link the
// mail carries — the nonce inside the token and the expiry the signature names, not a second
// computation of "now plus fifteen minutes" that could disagree by a second.
func TestIssueRecordsTheLinkItsOwnSignatureNames(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)

	if err := f.issuer.Issue(ctx, issuerTypedEmail); err != nil {
		t.Fatalf("Issue() = unexpected error %v", err)
	}

	if len(f.store.saved) != 1 {
		t.Fatalf("the issuer saved %d rows, want exactly 1", len(f.store.saved))
	}
	saved := f.store.saved[0]
	if saved.Email != issuerNormalizedEmail {
		t.Fatalf("the saved row names %q, want the normalized %q: two spellings of one "+
			"mailbox must not become two accounts", saved.Email, issuerNormalizedEmail)
	}
	if len(saved.Nonce) != 36 || strings.Count(saved.Nonce, "-") != 4 {
		t.Fatalf("the saved nonce %q is not the uuid the project mints identifiers with", saved.Nonce)
	}

	if len(f.limiter.keys) != 1 {
		t.Fatalf("the counter was asked %d times, want exactly once per request", len(f.limiter.keys))
	}
	if f.limiter.keys[0] != issuerRateLimitKeyValue {
		t.Fatalf("the counter key is %q, want %q", f.limiter.keys[0], issuerRateLimitKeyValue)
	}

	mail := onlyMail(t, f.mailer)
	if mail.To != issuerNormalizedEmail {
		t.Fatalf("the message goes to %q, want the normalized %q", mail.To, issuerNormalizedEmail)
	}

	claims, err := f.signer.Verify(mailedToken(t, mail), time.Now().UTC())
	if err != nil {
		t.Fatalf("Verify() of the mailed token = unexpected error %v", err)
	}
	if claims.Nonce != saved.Nonce {
		t.Fatalf("the mail carries nonce %q while the row recorded %q: a redeemed link would "+
			"find no row, or the wrong one", claims.Nonce, saved.Nonce)
	}
	if claims.Email != saved.Email {
		t.Fatalf("the mail is signed for %q while the row names %q", claims.Email, saved.Email)
	}
	if !claims.ExpiresAt.Equal(saved.ExpiresAt) {
		t.Fatalf("the row expires at %v while the signature expires at %v: the row and the "+
			"link must end at the same instant", saved.ExpiresAt, claims.ExpiresAt)
	}
	if window := time.Until(saved.ExpiresAt); window > accessTTL+time.Minute || window < accessTTL-time.Minute {
		t.Fatalf("the link lives %s, want about the signer's %s: an access link is a step, "+
			"not a key", window, accessTTL)
	}

	// Two requests in the same second are two links: the nonce, not the clock, is what makes
	// them different, and a spent one must not spend the other.
	if err := f.issuer.Issue(ctx, issuerTypedEmail); err != nil {
		t.Fatalf("second Issue() = unexpected error %v", err)
	}
	if len(f.store.saved) != 2 || f.store.saved[1].Nonce == saved.Nonce {
		t.Fatalf("two issues recorded the same nonce %q, so one login step would spend the other",
			saved.Nonce)
	}
}

// S2: the message is its own text and not the ticket email's. It names the minutes, it says
// the link opens once, it says ignoring it is safe, and it carries the URL — which means it
// carries the token.
func TestTheLoginEmailIsItsOwnMessage(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)

	if err := f.issuer.Issue(ctx, issuerNormalizedEmail); err != nil {
		t.Fatalf("Issue() = unexpected error %v", err)
	}

	mail := onlyMail(t, f.mailer)
	token := mailedToken(t, mail)
	if !strings.Contains(mail.Body, token) {
		t.Fatalf("the body does not carry its own token")
	}
	for _, want := range []string{"15 minutes", "once", "ignore"} {
		if !strings.Contains(strings.ToLower(mail.Body), want) {
			t.Fatalf("the body does not say %q, which is what makes it a login email and not "+
				"a ticket email:\n%s", want, mail.Body)
		}
	}
	if strings.Contains(mail.Body, "Your tickets are here") {
		t.Fatalf("the login email reuses the ticket email's text:\n%s", mail.Body)
	}
	if strings.ContainsAny(mail.Body, "<>") {
		t.Fatal("the login email carries markup: it is plain text, a URL and a warning")
	}
	if mail.Subject == "" || !strings.Contains(strings.ToLower(mail.Subject), "sign") {
		t.Fatalf("the subject %q does not say what the message is", mail.Subject)
	}
}

// The address is normalized defensively, and a blank one is refused before the counter is
// spent: an empty address would share one counter key with every other blank request, which
// is how one caller's allowance would pay for another's.
func TestIssueRefusesABlankAddressBeforeTheCounter(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)

	err := f.issuer.Issue(ctx, "   ")
	if !errors.Is(err, identity.ErrUnsignable) {
		t.Fatalf("Issue() of a blank address = %v, want ErrUnsignable", err)
	}
	if len(f.limiter.keys) != 0 {
		t.Fatalf("the counter was spent on a request with no address: %v", f.limiter.keys)
	}
	if len(f.store.saved) != 0 || len(f.mailer.sent) != 0 {
		t.Fatalf("a refused address wrote %d rows and sent %d messages, want none",
			len(f.store.saved), len(f.mailer.sent))
	}
}

// S6: a counter that refuses sends nothing, and answers the same error an unreadable counter
// answers — one refusal, so the caller cannot turn "you asked too often" into an oracle for
// whether an address has an account.
func TestARefusedLimiterSendsNothingAndSaysOnlyOneThing(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)
	f.limiter.allowed, f.limiter.retryAfter = false, 5*time.Minute

	err := f.issuer.Issue(ctx, issuerTypedEmail)
	if !errors.Is(err, identity.ErrLinkRateLimited) {
		t.Fatalf("Issue() inside the window = %v, want ErrLinkRateLimited", err)
	}
	if len(f.store.saved) != 0 {
		t.Fatalf("a refused request wrote %d rows, want none", len(f.store.saved))
	}
	if len(f.mailer.sent) != 0 {
		t.Fatalf("a refused request sent %d messages, want none", len(f.mailer.sent))
	}
	assertNamesNoAddress(t, err.Error())
	if strings.Contains(f.logs.String(), issuerNormalizedEmail) {
		t.Fatalf("the log names the address: %s", f.logs.String())
	}
}

// S6, fail closed: an unreadable counter is not permission. The send is refused, and the
// refusal is the same one a spent allowance answers, because telling a stranger that our
// counter is down is a different fact about somebody's address leaking out of an outage.
func TestAnUnreadableCounterRefusesTheSendAndStillSaysOneThing(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)
	f.limiter.err = errors.New("redis is down")

	err := f.issuer.Issue(ctx, issuerTypedEmail)
	if !errors.Is(err, identity.ErrLinkRateLimited) {
		t.Fatalf("Issue() with an unreadable counter = %v, want ErrLinkRateLimited: an "+
			"unreadable counter must never be read as permission", err)
	}
	if err.Error() != identity.ErrLinkRateLimited.Error() {
		t.Fatalf("the refusal says %q, which a caller could tell apart from a spent "+
			"allowance", err.Error())
	}
	if len(f.store.saved) != 0 || len(f.mailer.sent) != 0 {
		t.Fatalf("a request with no readable counter wrote %d rows and sent %d messages, "+
			"want none", len(f.store.saved), len(f.mailer.sent))
	}
	// The outage is still reported to whoever runs the deployment, without the address.
	if !strings.Contains(f.logs.String(), "could not be read") {
		t.Fatalf("the operator learns nothing about the counter failing: %s", f.logs.String())
	}
	if strings.Contains(f.logs.String(), issuerNormalizedEmail) {
		t.Fatalf("the log names the address: %s", f.logs.String())
	}
}

// The mail must not leave for a link that was not recorded: an unrecorded link can never be
// spent, so mailing it would hand out a URL that works until it expires, as often as anybody
// presents it.
func TestAStoreFailureLeavesTheMailUnsent(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)
	storeErr := errors.New("the database is down")
	f.store.err = storeErr

	err := f.issuer.Issue(ctx, issuerTypedEmail)
	if !errors.Is(err, storeErr) {
		t.Fatalf("Issue() with an unwritable store = %v, want the store's failure", err)
	}
	if errors.Is(err, identity.ErrLinkRateLimited) {
		t.Fatalf("a store failure is reported as a throttled request: %v", err)
	}
	if len(f.mailer.sent) != 0 {
		t.Fatalf("a link that was not recorded was mailed %d times, want none", len(f.mailer.sent))
	}
	assertNamesNoAddress(t, err.Error())
}

// S2, S3: the address is a person and the token is a credential, so neither may be written
// anywhere an operator or a log collector reads — on the success path or on a refusal.
func TestIssuingNeverLogsTheAddressOrTheToken(t *testing.T) {
	ctx := context.Background()
	f := newIssueFixture(t)

	if err := f.issuer.Issue(ctx, issuerTypedEmail); err != nil {
		t.Fatalf("Issue() = unexpected error %v", err)
	}
	token := mailedToken(t, onlyMail(t, f.mailer))
	assertDoesNotName(t, f.logs.String(), issuerNormalizedEmail, token, issuerLinkBase+token)

	f.logs.Reset()
	f.limiter.allowed = false
	if err := f.issuer.Issue(ctx, issuerTypedEmail); err == nil {
		t.Fatal("Issue() inside the window = nil, want a refusal")
	}
	assertDoesNotName(t, f.logs.String(), issuerNormalizedEmail, token, issuerLinkBase+token)
}

// A wiring that could not send anything is refused at construction, naming the missing
// piece, instead of failing the first person who asks for a link.
func TestNewIssuerRefusesWhatCannotWork(t *testing.T) {
	signer := mustAccessSigner(t, accessTestKey, accessTTL)
	store := &fakeAccessLinkStore{}
	limiter := &fakeLinkLimiter{allowed: true}
	mailer := &fakeMailer{}

	cases := []struct {
		name    string
		signer  *identity.AccessSigner
		store   identity.AccessLinkStore
		limiter identity.LinkLimiter
		mailer  identity.Mailer
		link    string
	}{
		{"no signer", nil, store, limiter, mailer, issuerLinkBase},
		{"no store", signer, nil, limiter, mailer, issuerLinkBase},
		{"no counter", signer, store, nil, mailer, issuerLinkBase},
		{"no mailer", signer, store, limiter, nil, issuerLinkBase},
		{"no link base", signer, store, limiter, mailer, "   "},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if issuer, err := identity.NewIssuer(c.signer, c.store, c.limiter, c.mailer, c.link, nil); err == nil {
				t.Fatalf("NewIssuer() = %v, nil; want a refusal", issuer)
			}
		})
	}
}

// assertNamesNoAddress pins that an error text a caller may log carries no person.
func assertNamesNoAddress(t *testing.T, text string) {
	t.Helper()
	assertDoesNotName(t, text, issuerNormalizedEmail, "@example.test", "identity:link:")
}

// assertDoesNotName pins that text carries none of the given values.
func assertDoesNotName(t *testing.T, text string, values ...string) {
	t.Helper()
	for _, value := range values {
		if value != "" && strings.Contains(text, value) {
			t.Fatalf("%q carries %q", text, value)
		}
	}
}
