// The SMTP adapter: the one place this package talks to a mail server.
//
// It exists so the worker depends on [Sender] and not on a protocol. Mailpit in local
// development and a real provider in a demo are the same adapter with a different address;
// nothing above this file changes between them.
package email

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// SMTP delivers through a plain SMTP server: Mailpit on 127.0.0.1:1025 locally, a provider
// elsewhere. It authenticates with nothing, because Mailpit needs nothing and a real
// provider that needs credentials is a deliberate later step, not something to invent here.
type SMTP struct {
	addr string
	from string
}

// NewSMTP builds the adapter, refusing an address it could not dial or a sender it could
// not name.
func NewSMTP(addr, from string) (*SMTP, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || host == "" || port == "" {
		return nil, fmt.Errorf("%w: the SMTP address must be host:port, got %q", ErrNotConfigured, addr)
	}
	if strings.TrimSpace(from) == "" {
		return nil, fmt.Errorf("%w: the sender address is missing", ErrNotConfigured)
	}
	return &SMTP{addr: addr, from: from}, nil
}

// Send delivers one message.
//
// `net/smtp` has no context: an SMTP conversation cannot be cancelled halfway, and pretending
// otherwise by checking the context first would be a promise this cannot keep. It refuses to
// start when the context is already done, and says so here rather than in a comment nobody
// reads.
func (s *SMTP) Send(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(message.To) == "" {
		return fmt.Errorf("%w: a message needs a recipient", ErrNotConfigured)
	}
	// The address arrived from a buyer, so it is checked for the one thing that would let it
	// rewrite the message: a line break. smtp.SendMail does not sanitise headers.
	if strings.ContainsAny(message.To, "\r\n") || strings.ContainsAny(s.from, "\r\n") {
		return fmt.Errorf("%w: an address must not contain a line break", ErrNotConfigured)
	}

	raw := strings.Join([]string{
		"From: " + s.from,
		"To: " + message.To,
		"Subject: " + message.Subject,
		"Date: " + time.Now().UTC().Format(time.RFC1123Z),
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		message.Body,
	}, "\r\n")

	if err := smtp.SendMail(s.addr, nil, s.from, []string{message.To}, []byte(raw)); err != nil {
		return fmt.Errorf("%w: send the message: %v", ErrUnavailable, err)
	}
	return nil
}

// Addr is the address this adapter dials, for the startup log. It is not a secret: a local
// Mailpit and a hosted provider are both an address and a port.
func (s *SMTP) Addr() string { return s.addr }
