// The identity routes live in this file: POST /api/v1/identity/links asks for a sign-in link by
// mail, POST /api/v1/identity/sessions spends one and answers a session token, and
// DELETE /api/v1/identity/sessions ends the session that token opened (T2, T3, S1, S2, S3, S6).
//
// They are the transport half of the login the identity package already implements. That
// package owns the signature, the nonce, the transaction and the session; what is written
// here is only what the wire is allowed to know, and it is written around one rule: a public
// route that makes this deployment mail somebody must not become a question anybody can ask
// about an address.
//
// # Asking for a link says one thing
//
// `202 Accepted` with `{"status": "accepted"}` — for a link that was minted and mailed, for an
// address that is inside its allowance, and for a counter that could not be read. Three
// different things, one answer, byte for byte. The alternative was to be helpful: a `429` for
// the refused allowance, a `503` for the unreadable counter, a different wording for an
// address no account has ever used. Each of those is an oracle. "This address asked too
// often" tells a stranger the address exists in somebody's life, and the whole login screen
// exists to withhold that. The refusal is real — nothing is signed and nothing is mailed — it
// is simply the operator's fact and not the caller's, so it is written to the log and not to
// the answer.
//
// `202` and not `200` because nothing is known yet: the message is being delivered by whoever
// runs SMTP, and the person's next step is to open their inbox, not to read a body here.
//
// # A request that is not a request is a 400
//
// A body that is not `{"email": "…"}`, an unknown field, an address the checkout's own rule
// refuses: `400 invalid_input`. This is the one case that differs, and it is safe to differ
// because it is a statement about the envelope and not about any account — the address was
// never normalized, never counted and never mailed. The rule is `checkout.NormalizeContactEmail`,
// imported rather than restated, so the purchase form and the login form cannot disagree about
// what an address is.
//
// # Spending a link answers a token, once
//
// `201 Created` with `{"token": …, "expiresAt": …}` — the only time the session token is ever
// returned. It is in the body and not in a cookie because **this API sets no cookie**: the
// browser never talks to it (the web does), the session cookie is emitted and read by the web
// on its own origin, and each server action presents the token here as the credential it is.
// A cookie set by this origin would be a second session store the web cannot read, and the
// study's D2/S1 put the cookie on the web's side on purpose.
//
// # Every way a link fails to redeem is one refusal
//
// A spent link, an expired one and a forged one are one `401 unauthorized` with one fixed
// body. Telling them apart would confirm which links this deployment once issued, and — for a
// link somebody is watching — when its owner used it. The domain already collapses the three
// into opaque errors; this file forwards that opacity instead of enriching it.
//
// # Signing out answers one thing, and it is not a question
//
// `DELETE /identity/sessions` presents the session token and answers `204 No Content` for a
// live token, an expired one, a forged one and a request with no credential at all — see
// [Handler.closeIdentitySession]. The client is told its sign-out worked, which is true in the
// only sense it can act on: this credential no longer opens a session here. Nothing about a
// token, a hash or a row reaches the answer or the log.
//
// # Our failures are 503 and our secrets stay out of the logs
//
// A store that cannot answer, a signer that cannot be used, a provider that cannot send:
// `503 service_unavailable`, retryable, and never a verdict on the address or the link. The
// one log line the link request writes names the outcome only — `issued`, `rate_limited`,
// `unavailable` — and never the address, the token, the link or a key.

package publicapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"

	"cinema.local/ticketing/apps/api/internal/checkout"
	"cinema.local/ticketing/apps/api/internal/identity"
)

// IdentityLinkService is the issuing half of the login: mint one signed link for one address,
// count the request against it and mail it. It is satisfied by the identity package's
// [identity.Issuer]; a deployment with no signing key or no mail provider wires nothing, and
// then neither identity route is mounted.
//
// It answers [identity.ErrLinkRateLimited] for both a spent allowance and a counter that could
// not be read, [identity.ErrUnsignable] for an address the signed payload cannot carry, and
// its own error for a store or provider failure. The mapping to statuses lives below.
type IdentityLinkService interface {
	Issue(ctx context.Context, email string) error
}

// IdentitySessionService is the redeeming half: spend one presented link and answer the
// session token it opens, exactly once, beside how many older orders the verified address
// claimed. It is satisfied by [identity.Redeemer].
//
// The claimed count is answered to this layer and not published by it: D5 asks for the claim to
// be recorded, and the record is the one line the redeemer writes — not a field in a body whose
// only reason to exist is to carry a credential. A caller is told what it must present next, not
// how much history its mailbox turned out to have.
//
// It answers the signer's own opaque refusals ([identity.ErrInvalidToken],
// [identity.ErrAccessLinkExpired]), the store's one [identity.ErrLinkSpent], and
// [identity.ErrLinkUnavailable] or [identity.ErrInvalidKey] when the deployment cannot answer
// at all — which the route maps to a retryable failure, never to a refusal of the credential.
type IdentitySessionService interface {
	Redeem(ctx context.Context, token string) (string, identity.Session, int, error)
}

// identityLinkRoute, identitySessionRoute and identitySignOutRoute are the route patterns,
// relative to the /api/v1 mount point. The last two share a path and differ by method, which is
// the difference between spending a link for a session and ending one: one is an act that needs
// a request body, the other is the absence of an act.
const (
	identityLinkRoute    = "POST /identity/links"
	identitySessionRoute = "POST /identity/sessions"
	identitySignOutRoute = "DELETE /identity/sessions"
)

// maxIdentityBodyBytes bounds the one field each route reads, so a client cannot make the
// server buffer a body it has no use for.
const maxIdentityBodyBytes = 1 << 10

// WithIdentity attaches both halves of the login and returns the same handler, so the routes
// are mounted only when the surface behind them exists. The sign-out is attached beside it, by
// [Handler.WithIdentitySignOut]: it needs the session service rather than the two halves of the
// link flow.
//
// The two routes are mounted as a pair and unmounted as a pair: a link that could be asked
// for and never redeemed, or a redeem endpoint with no way to obtain a link, is worse than an
// absent surface — the first advertises a login that cannot be finished and the second a
// login nobody can start. A nil collaborator therefore leaves both unmounted (see
// [Handler.Mux]).
func (h *Handler) WithIdentity(issuer IdentityLinkService, redeemer IdentitySessionService) *Handler {
	h.identityLinks = issuer
	h.identitySessions = redeemer
	return h
}

// IdentitySessionCloser ends the session a presented token opens. It is satisfied by
// [identity.SessionService] — the same service the account list authenticates with, because a
// session is one row and one service is what reads and closes it.
//
// It answers nil for every token that closes nothing — already revoked, expired, never issued,
// or not even shaped like a token — and an error only when the store could not be reached, which
// is the one case where the row really is still open.
type IdentitySessionCloser interface {
	Close(ctx context.Context, token string) error
}

// WithIdentitySignOut attaches the sign-out and returns the same handler, so the route that ends
// a session is mounted only where the login that opens sessions is.
//
// It is a separate attachment rather than a third argument of [Handler.WithIdentity] because the
// two halves are different capabilities: a deployment can serve the login and have no session
// service to close through, and then this route is absent instead of mounted against nothing.
// Where the login is wired — cmd/api always wires both — both are.
func (h *Handler) WithIdentitySignOut(sessions IdentitySessionCloser) *Handler {
	h.identitySignOut = sessions
	return h
}

// issueIdentityLink asks the issuer to mint and mail one link, and answers the same 202 for
// every outcome that says anything about the address.
//
// The body is validated first, so a malformed request costs no counter and no read — and,
// more importantly, so a `400` cannot be reached with an address this deployment then counts,
// which would make the refusal itself a way to spend somebody's allowance.
func (h *Handler) issueIdentityLink(w http.ResponseWriter, r *http.Request) {
	address, err := identityLinkAddress(r)
	if err != nil {
		// Not logged: a malformed request is not an outcome of the login, it is a client
		// mistake refused before this route did anything, exactly like the resend route
		// beside it.
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
		return
	}

	if err := h.identityLinks.Issue(r.Context(), address); err != nil {
		switch {
		case errors.Is(err, identity.ErrLinkRateLimited):
			// A spent allowance and a counter that could not be read both stop here, and both
			// answer exactly like a sent link: see the file comment. Nothing was signed and
			// nothing was mailed, which is the fact the operator gets and the caller does not.
			logIdentityLinkOutcome("rate_limited")
			writeIdentityLinkAccepted(w)
		case errors.Is(err, identity.ErrUnsignable):
			// The address passed the checkout's rule and still cannot be a signed payload —
			// the separator a payload joins its fields with, for instance. That is a property
			// of the request and not of an account, so it is safe to say 400.
			writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input",
				"email is not an address this sign-in link can be sent to")
		default:
			// A store or a provider that could not answer. Retryable, and revealing nothing
			// about the address: an outage must never look like a decision about a mailbox.
			logIdentityLinkOutcome("unavailable")
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"the sign-in link cannot be sent right now; retry the request")
		}
		return
	}

	logIdentityLinkOutcome("issued")
	writeIdentityLinkAccepted(w)
}

// redeemIdentitySession spends one presented link and answers the session it opened.
//
// The token is read from the body and not from the path or a header: it arrives from the web's
// own server-to-server call, not from a browser navigation, so there is no URL for it to end
// up in — and a credential in a path spends its life in access logs and `Referer` headers,
// which is why the ticket link route above says the same thing about its own token.
func (h *Handler) redeemIdentitySession(w http.ResponseWriter, r *http.Request) {
	token, err := identitySessionPresented(r)
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "invalid_input", "Invalid input", err.Error())
		return
	}

	// The claimed count is read and dropped: it is the operator's fact, not the caller's. See
	// IdentitySessionService.
	sessionToken, session, _, err := h.identitySessions.Redeem(r.Context(), token)
	if err != nil {
		switch {
		case errors.Is(err, identity.ErrLinkSpent), errors.Is(err, identity.ErrAccessLinkExpired),
			errors.Is(err, identity.ErrInvalidToken):
			// Spent, expired, forged: one answer, byte for byte. See the file comment.
			writeIdentityUnauthorized(w)
		default:
			// A store, a signer or something nobody explained: our failure and retryable. A
			// database that is down must never look like a link somebody already used.
			writeProblem(w, http.StatusServiceUnavailable, "service_unavailable", "Service unavailable",
				"the sign-in link cannot be redeemed right now; retry the request")
		}
		return
	}

	// The row is what an operator can join on, and it is not a credential: the token is never
	// logged and never appears here.
	slog.Default().Info("identity session opened", "session", session.ID)

	// The token is answered once, to the caller that will put it in the web's cookie. The
	// expiry is read from the session the store wrote, so the client cannot be told a date the
	// row does not keep.
	writeJSON(w, http.StatusCreated, map[string]any{
		"token":     sessionToken,
		"expiresAt": session.ExpiresAt.UTC(),
	})
}

// The two outcomes a sign-out records.
//
// They are not the domain's three states, and cannot be: [identity.SessionService.Close] answers
// "you are signed out" both for a row it just revoked and for a token there was nothing to close
// — that identity is what keeps sign-out from being a way to ask whether a credential existed —
// so this layer cannot tell them apart without the second lookup that would defeat the point.
// What it *can* tell apart is a store that did not answer, and that is the one fact an operator
// needs, because in that case the row is still live.
const (
	identitySessionClosed           = "closed"
	identitySessionCloseUnavailable = "unavailable"
)

// closeIdentitySession ends the session the presented bearer token opens, and answers
// `204 No Content` in every case it answers at all: a live token, an expired one, a revoked one,
// a forged one, a string that is not a token, and a request with no Authorization header.
//
// Both halves of that are deliberate.
//
//   - The request is not a question. [identity.SessionService.Close] is quiet by construction: a
//     client told its sign-out failed believes it is still signed in, and a row it could not
//     reach is one it cannot do anything about anyway. The transport forwards that quietness
//     rather than inventing a verdict on top of it.
//   - An error status would be the one answer this surface never gives. A `401` here would tell
//     a caller whether a credential ever existed, which is exactly what the account list's fixed
//     refusal withholds: spent, expired, revoked and never issued are one answer there, and a
//     different one here for the same token would make signing out the way to ask.
//
// A no-op is therefore a truthful answer for a token there is nothing to close, which is why the
// header is read and not required: no request to this route is refused, so no request to it can
// be probed with.
func (h *Handler) closeIdentitySession(w http.ResponseWriter, r *http.Request) {
	// The header is read by the same helper the account list uses: one reading of "what bearer
	// was presented" for the two routes that take a session, so they cannot disagree about it.
	// A header that carries no bearer is the empty string, which the domain refuses as a token
	// shape before it costs a query.
	outcome := identitySessionClosed
	if err := h.identitySignOut.Close(r.Context(), bearerToken(r)); err != nil {
		// Our store did not answer, so the row is in fact still open. The client is not told —
		// see above — and the operator is, in the one line every sign-out writes.
		outcome = identitySessionCloseUnavailable
	}
	logIdentitySessionClosed(outcome)
	// No body and no content type: there is nothing to say about a credential that is closed,
	// and nothing to hand back for one that was never a credential.
	w.WriteHeader(http.StatusNoContent)
}

// writeIdentityLinkAccepted is the one answer the link request gives for a sent link, a
// refused allowance and an unreadable counter. It is a single function so the three paths
// cannot drift into three spellings of "we are not telling you".
func writeIdentityLinkAccepted(w http.ResponseWriter) {
	writeJSON(w, http.StatusAccepted, map[string]any{"status": "accepted"})
}

// writeIdentityUnauthorized is the one refusal the redeem route gives for a spent, an expired
// and a forged link: one status, one body, no detail that could tell them apart and no echo of
// the token.
func writeIdentityUnauthorized(w http.ResponseWriter) {
	writeProblem(w, http.StatusUnauthorized, "unauthorized", "Unauthorized",
		"this sign-in link cannot be used")
}

// logIdentityLinkOutcome writes the one line a link request records. It names the outcome and
// nothing else: the address is a person, the token and the link are credentials, and the
// counter's own error can name the key that carries the address — so none of them is here.
func logIdentityLinkOutcome(outcome string) {
	slog.Default().Info("identity link request", "outcome", outcome)
}

// logIdentitySessionClosed writes the one line a sign-out records: its outcome and nothing else.
//
// No token and no hash of one — the log outlives the request, and both of those are credentials
// or the seed of one — and no session identifier either, because Close answers none and an
// identifier printed from somewhere else would be a key nobody asked the store for.
func logIdentitySessionClosed(outcome string) {
	slog.Default().Info("identity session closed", "outcome", outcome)
}

// identityLinkAddress reads the address to mail from the request body.
//
// The body is required, bounded and refuses unknown fields for the reasons the resend route
// beside this one states: `{"emial": …}` would silently become "no address", and a request
// that names something this route does not read should be told so rather than dropped.
func identityLinkAddress(r *http.Request) (string, error) {
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeIdentityBody(r, &body); err != nil {
		return "", err
	}

	// The checkout's rule, imported rather than restated: two answers to "is this an address"
	// can only drift apart, and the one that drifts is the one nobody is looking at.
	address, err := checkout.NormalizeContactEmail(body.Email)
	if err != nil {
		return "", err
	}
	if address == "" {
		return "", fmt.Errorf("%w: email is required: there is no account to fall back on", checkout.ErrInvalidInput)
	}
	return address, nil
}

// identitySessionPresented reads the link to redeem from the request body, bounded and
// unknown-field-refusing in the same way.
func identitySessionPresented(r *http.Request) (string, error) {
	var body struct {
		Token string `json:"token"`
	}
	if err := decodeIdentityBody(r, &body); err != nil {
		return "", err
	}
	if body.Token == "" {
		return "", fmt.Errorf("%w: token is required", checkout.ErrInvalidInput)
	}
	return body.Token, nil
}

// decodeIdentityBody reads one bounded JSON object of the documented shape.
//
// The shape is checked before anything is counted, signed or looked up, so a request that is
// not the documented object costs this deployment nothing but the read.
func decodeIdentityBody(r *http.Request, body any) error {
	if r.Body == nil {
		return fmt.Errorf("%w: the request body is required", checkout.ErrInvalidInput)
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxIdentityBodyBytes))
	if err != nil {
		return fmt.Errorf("%w: the request body could not be read", checkout.ErrInvalidInput)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(body); err != nil {
		return fmt.Errorf("%w: the request body is not the documented JSON object", checkout.ErrInvalidInput)
	}
	return nil
}
