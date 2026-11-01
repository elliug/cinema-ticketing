// Behaviour tests for the ticket email consumer.
//
// The logic worth testing here is not "does it call the sender": it is what happens on the
// three edges that decide whether a buyer gets a mail or gets it twice — no address, a
// provider that fails, and a row already delivered.
package email_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/email"
)

// fakeStore is the durable half, in memory.
type fakeStore struct {
	rows       []email.Row
	recipients map[string]string
	delivered  []string

	pendingErr   error
	recipientErr error
	deliveredErr error
}

func (s *fakeStore) Pending(_ context.Context, _ string, limit int) ([]email.Row, error) {
	if s.pendingErr != nil {
		return nil, s.pendingErr
	}
	if limit < len(s.rows) {
		return s.rows[:limit], nil
	}
	return s.rows, nil
}

func (s *fakeStore) Recipient(_ context.Context, reservationID string) (string, error) {
	if s.recipientErr != nil {
		return "", s.recipientErr
	}
	return s.recipients[reservationID], nil
}

func (s *fakeStore) Delivered(_ context.Context, _ string, outboxID string) error {
	if s.deliveredErr != nil {
		return s.deliveredErr
	}
	s.delivered = append(s.delivered, outboxID)
	return nil
}

// fakeSender records what it was asked to send and can fail the nth message.
type fakeSender struct {
	messages []email.Message
	failWith error
}

func (s *fakeSender) Send(_ context.Context, message email.Message) error {
	if s.failWith != nil {
		return s.failWith
	}
	s.messages = append(s.messages, message)
	return nil
}

const testLink = "https://tickets.example/t/v1.signed"

func newWorker(t *testing.T, store email.Store, sender email.Sender) *email.Worker {
	t.Helper()
	worker, err := email.NewWorker(store, sender, func(string) (string, error) { return testLink, nil }, email.WorkerConfig{
		Interval: time.Minute,
		Batch:    10,
		Timeout:  time.Second,
		From:     "entradas@example.com",
	})
	if err != nil {
		t.Fatalf("build the worker: %v", err)
	}
	return worker
}

func row(id, reservationID string) email.Row {
	return email.Row{ID: id, Payload: []byte(`{"reservationId":"` + reservationID + `"}`)}
}

// TestPassDeliversAndRecords: the ordinary case, asserted on the message rather than on the
// call count, because the point is what the buyer receives.
func TestPassDeliversAndRecords(t *testing.T) {
	store := &fakeStore{
		rows:       []email.Row{row("outbox-1", "res-1")},
		recipients: map[string]string{"res-1": "compras@example.com"},
	}
	sender := &fakeSender{}
	worker := newWorker(t, store, sender)

	if err := worker.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}

	if len(sender.messages) != 1 {
		t.Fatalf("sent %d message(s), want 1", len(sender.messages))
	}
	message := sender.messages[0]
	if message.To != "compras@example.com" {
		t.Fatalf("To = %q, want the stored address", message.To)
	}
	if message.Subject == "" {
		t.Fatal("the message has no subject")
	}
	if !strings.Contains(message.Body, testLink) {
		t.Fatalf("the body does not carry the ticket link:\n%s", message.Body)
	}
	// The link is a credential, so the message has to say so rather than pretending it is
	// an ordinary receipt.
	if !strings.Contains(strings.ToLower(message.Body), "anyone with the link") {
		t.Fatalf("the body does not warn that the link is a credential:\n%s", message.Body)
	}
	if len(store.delivered) != 1 || store.delivered[0] != "outbox-1" {
		t.Fatalf("delivered = %v, want the row recorded once", store.delivered)
	}
}

// TestPassRecordsARowWithNoAddressWithoutSending: the buyer gave no address, so there is
// nothing to send — and the row must still be recorded, or every pass would retry it for
// ever and the outbox would grow without ever delivering anything.
func TestPassRecordsARowWithNoAddressWithoutSending(t *testing.T) {
	store := &fakeStore{rows: []email.Row{row("outbox-1", "res-1")}, recipients: map[string]string{}}
	sender := &fakeSender{}
	worker := newWorker(t, store, sender)

	if err := worker.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(sender.messages) != 0 {
		t.Fatalf("sent %d message(s) for a reservation with no address, want none", len(sender.messages))
	}
	if len(store.delivered) != 1 {
		t.Fatalf("delivered = %v, want the row recorded so it stops being retried", store.delivered)
	}
}

// TestPassLeavesAFailedSendUndelivered is the promise that a provider outage costs a retry
// and not a ticket: nothing is recorded, so the next pass tries again.
func TestPassLeavesAFailedSendUndelivered(t *testing.T) {
	store := &fakeStore{
		rows:       []email.Row{row("outbox-1", "res-1")},
		recipients: map[string]string{"res-1": "compras@example.com"},
	}
	sender := &fakeSender{failWith: errors.New("smtp: connection refused")}
	worker := newWorker(t, store, sender)

	if err := worker.Pass(context.Background()); err != nil {
		t.Fatalf("pass returned %v, want a logged row failure rather than a failed pass", err)
	}
	if len(store.delivered) != 0 {
		t.Fatalf("delivered = %v, want nothing recorded so the next pass retries", store.delivered)
	}
}

// TestPassKeepsGoingAfterOneFailure: a broken row must not starve the ones behind it.
func TestPassKeepsGoingAfterOneFailure(t *testing.T) {
	store := &fakeStore{
		rows:       []email.Row{row("outbox-1", "res-1"), row("outbox-2", "res-2")},
		recipients: map[string]string{"res-1": "uno@example.com", "res-2": "dos@example.com"},
	}
	// The first send fails, the second must still happen: the fake fails once.
	sender := &onceFailingSender{}
	worker := newWorker(t, store, sender)

	if err := worker.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(store.delivered) != 1 || store.delivered[0] != "outbox-2" {
		t.Fatalf("delivered = %v, want exactly the row that was sent", store.delivered)
	}
}

// onceFailingSender fails its first message and succeeds afterwards.
type onceFailingSender struct{ calls int }

func (s *onceFailingSender) Send(_ context.Context, _ email.Message) error {
	s.calls++
	if s.calls == 1 {
		return errors.New("smtp: temporary failure")
	}
	return nil
}

// TestUndecodablePayloadIsRecorded: a payload this worker can never read must not be
// retried for ever. The producer validates what it writes, so this is a defect to surface,
// not a routine case — and surfacing it is the log, not an endless retry.
func TestUndecodablePayloadIsRecorded(t *testing.T) {
	store := &fakeStore{
		rows:       []email.Row{{ID: "outbox-1", Payload: []byte("not json")}},
		recipients: map[string]string{},
	}
	sender := &fakeSender{}
	worker := newWorker(t, store, sender)

	if err := worker.Pass(context.Background()); err != nil {
		t.Fatalf("pass: %v", err)
	}
	if len(store.delivered) != 1 {
		t.Fatalf("delivered = %v, want the unreadable row recorded", store.delivered)
	}
}

// TestNewWorkerRefusesAnImpossibleConfiguration pins the named refusals, so a deployment
// that forgot the sender address learns it at startup instead of on the first ticket.
func TestNewWorkerRefusesAnImpossibleConfiguration(t *testing.T) {
	cases := []struct {
		name   string
		store  email.Store
		sender email.Sender
		link   email.LinkBuilder
		cfg    email.WorkerConfig
	}{
		{"no store", nil, &fakeSender{}, func(string) (string, error) { return "", nil }, validConfig()},
		{"no sender", &fakeStore{}, nil, func(string) (string, error) { return "", nil }, validConfig()},
		{"no link builder", &fakeStore{}, &fakeSender{}, nil, validConfig()},
		{"no interval", &fakeStore{}, &fakeSender{}, func(string) (string, error) { return "", nil }, email.WorkerConfig{Batch: 1, Timeout: time.Second, From: "a@b.com"}},
		{"no from", &fakeStore{}, &fakeSender{}, func(string) (string, error) { return "", nil }, email.WorkerConfig{Interval: time.Second, Batch: 1, Timeout: time.Second}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := email.NewWorker(tc.store, tc.sender, tc.link, tc.cfg); !errors.Is(err, email.ErrNotConfigured) {
				t.Fatalf("error = %v, want ErrNotConfigured", err)
			}
		})
	}
}

func validConfig() email.WorkerConfig {
	return email.WorkerConfig{Interval: time.Minute, Batch: 10, Timeout: time.Second, From: "entradas@example.com"}
}
