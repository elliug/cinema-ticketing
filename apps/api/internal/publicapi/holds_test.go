package publicapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"cinema.local/ticketing/apps/api/internal/config"
	"cinema.local/ticketing/apps/api/internal/health"
	"cinema.local/ticketing/apps/api/internal/httpserver"
	"cinema.local/ticketing/apps/api/internal/publicapi"
	"cinema.local/ticketing/apps/api/internal/reservation"
)

// fakeHoldService records what the transport forwarded to the application
// service and answers with a configured outcome.
type fakeHoldService struct {
	hold    reservation.Hold
	err     error
	seats   []reservation.SeatAvailability
	seatErr error

	calls               int
	key                 string
	eventID             string
	seatIDs             []int64
	availabilityEventID string
}

func (f *fakeHoldService) CreateHold(_ context.Context, idempotencyKey, eventID string, eventSeatIDs []int64) (reservation.Hold, error) {
	f.calls++
	f.key, f.eventID, f.seatIDs = idempotencyKey, eventID, eventSeatIDs
	return f.hold, f.err
}

func (f *fakeHoldService) ListAvailability(_ context.Context, eventID string) ([]reservation.SeatAvailability, error) {
	f.availabilityEventID = eventID
	return f.seats, f.seatErr
}

// mountedServer wires the server exactly as cmd/api will: the public subtree is
// mounted at /api/v1 and is never placed behind the administrative guard.
func mountedServer(t *testing.T, svc publicapi.HoldService) *httptest.Server {
	t.Helper()
	healthSvc := health.New(health.ProcessProbe{})
	server := httpserver.New(config.Config{}, healthSvc, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(server.WithPublic(publicapi.New(svc).Mux()).Handler())
	t.Cleanup(ts.Close)
	return ts
}

func postHold(t *testing.T, ts *httptest.Server, key, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/reservations/holds", strings.NewReader(body))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("POST /api/v1/reservations/holds: %v", err)
	}
	return response
}

type problemBody struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func decodeBody[T any](t *testing.T, response *http.Response) T {
	t.Helper()
	defer response.Body.Close()
	var decoded T
	if err := json.NewDecoder(response.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	return decoded
}

type holdBody struct {
	ID        string    `json:"id"`
	EventID   string    `json:"eventId"`
	SeatIDs   []int64   `json:"seatIds"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// Behaviour 1 (S7): the hold route is mounted at /api/v1/reservations/holds
// outside the administrative guard and answers 201 with the hold id, while the
// guarded admin subtree, health routes, and the availability route keep their
// behaviour.
func TestPublicRoutesAreMountedWithoutTheAdminGuard(t *testing.T) {
	expires := time.Date(2030, 1, 1, 12, 10, 0, 0, time.UTC)
	svc := &fakeHoldService{
		hold:  reservation.Hold{ID: "hold-1", EventID: "evt-1", SeatIDs: []int64{21, 22}, ExpiresAt: expires, Status: 201},
		seats: []reservation.SeatAvailability{{ID: 21, LayoutSeatID: "seat-a", State: "available", Held: true}},
	}
	ts := mountedServer(t, svc)

	response := postHold(t, ts, "key-1", `{"eventId":"evt-1","seatIds":["21","22"]}`)
	if response.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(response.Body)
		response.Body.Close()
		t.Fatalf("POST /api/v1/reservations/holds status = %d, want 201 (body %s)", response.StatusCode, body)
	}
	if svc.key != "key-1" || svc.eventID != "evt-1" || len(svc.seatIDs) != 2 || svc.seatIDs[0] != 21 || svc.seatIDs[1] != 22 {
		t.Fatalf("forwarded key %q event %q seats %v, want key-1 evt-1 [21 22]", svc.key, svc.eventID, svc.seatIDs)
	}
	body := decodeBody[holdBody](t, response)
	if body.ID != "hold-1" || body.EventID != "evt-1" || len(body.SeatIDs) != 2 || !body.ExpiresAt.Equal(expires) {
		t.Fatalf("hold response = %+v, want the created hold", body)
	}

	availability, err := http.Get(ts.URL + "/api/v1/events/evt-1/seats")
	if err != nil {
		t.Fatalf("GET /api/v1/events/evt-1/seats: %v", err)
	}
	if availability.StatusCode != http.StatusOK {
		availability.Body.Close()
		t.Fatalf("availability status = %d, want 200", availability.StatusCode)
	}
	if svc.availabilityEventID != "evt-1" {
		t.Fatalf("availability read for %q, want evt-1", svc.availabilityEventID)
	}
	availability.Body.Close()

	// Preserve: mounting the public subtree must not expose the admin subtree,
	// and it must not remove the unguarded health routes.
	admin, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/admin/cinemas", strings.NewReader("{}"))
	if err != nil {
		t.Fatalf("build admin request: %v", err)
	}
	adminResponse, err := http.DefaultClient.Do(admin)
	if err != nil {
		t.Fatalf("POST /api/v1/admin/cinemas: %v", err)
	}
	adminResponse.Body.Close()
	if adminResponse.StatusCode != http.StatusNotFound {
		t.Fatalf("admin route status = %d, want 404 without the guard admitting it", adminResponse.StatusCode)
	}
	live, err := http.Get(ts.URL + "/health/live")
	if err != nil {
		t.Fatalf("GET /health/live: %v", err)
	}
	live.Body.Close()
	if live.StatusCode != http.StatusOK {
		t.Fatalf("health live status = %d, want 200", live.StatusCode)
	}
}

// Behaviour 2: every domain error the service reports maps to its own status
// and problem code, so a conflict can never be reported as an expired window,
// and a transport failure can never be reported as a success.
func TestCreateHoldMapsErrorsToStatuses(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"created hold", nil, http.StatusCreated, ""},
		{"seat taken by another hold", reservation.ErrSeatsUnavailable, http.StatusConflict, "seat_unavailable"},
		{"idempotency key reused", fmt.Errorf("%w: %q", reservation.ErrIdempotencyKeyReused, "key-1"), http.StatusConflict, "idempotency_key_reused"},
		{"self conflicting claim", reservation.ErrDuplicateClaim, http.StatusConflict, "duplicate_seat"},
		{"own hold window expired", fmt.Errorf("%w: %s", reservation.ErrHoldWindowExpired, "its hold window already expired"), http.StatusBadRequest, "hold_window_expired"},
		{"invalid input", fmt.Errorf("%w: eventId must not be empty", reservation.ErrInvalidInput), http.StatusBadRequest, "invalid_input"},
		{"unknown event", reservation.ErrEventNotFound, http.StatusNotFound, "not_found"},
		{"coordination unavailable", fmt.Errorf("%w: dial tcp: connection refused", reservation.ErrUnavailable), http.StatusServiceUnavailable, "coordination_unavailable"},
		{"unclassified failure", errors.New("unexpected"), http.StatusServiceUnavailable, "coordination_unavailable"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := &fakeHoldService{
				hold: reservation.Hold{ID: "hold-1", EventID: "evt-1", SeatIDs: []int64{21}, Status: 201},
				err:  testCase.err,
			}
			response := postHold(t, mountedServer(t, svc), "key-1", `{"eventId":"evt-1","seatIds":[21]}`)
			if response.StatusCode != testCase.wantStatus {
				body, _ := io.ReadAll(response.Body)
				response.Body.Close()
				t.Fatalf("status = %d, want %d (body %s)", response.StatusCode, testCase.wantStatus, body)
			}
			if testCase.wantCode == "" {
				body := decodeBody[holdBody](t, response)
				if body.ID != "hold-1" {
					t.Fatalf("created hold id = %q, want hold-1", body.ID)
				}
				return
			}
			problem := decodeBody[problemBody](t, response)
			if problem.Code != testCase.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, testCase.wantCode)
			}
			if problem.Status != testCase.wantStatus {
				t.Fatalf("problem status field = %d, want %d", problem.Status, testCase.wantStatus)
			}
			if problem.Detail == "" || problem.Title == "" || problem.Type == "" {
				t.Fatalf("problem detail is incomplete: %+v", problem)
			}
		})
	}
}

// Behaviour 3: the transport validates its own envelope (JSON body, numeric
// seat identifiers) and forwards the idempotency key and payload unchanged, so
// the application service owns the domain validation.
func TestCreateHoldValidatesTheRequestEnvelope(t *testing.T) {
	t.Run("malformed JSON is rejected before the service", func(t *testing.T) {
		svc := &fakeHoldService{}
		response := postHold(t, mountedServer(t, svc), "key-1", `{"eventId":`)
		if response.StatusCode != http.StatusBadRequest {
			response.Body.Close()
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		problem := decodeBody[problemBody](t, response)
		if problem.Code != "invalid_request" {
			t.Fatalf("problem code = %q, want invalid_request", problem.Code)
		}
		if svc.calls != 0 {
			t.Fatalf("malformed body reached the service %d times", svc.calls)
		}
	})

	t.Run("non-numeric seat identifier is rejected before the service", func(t *testing.T) {
		svc := &fakeHoldService{}
		response := postHold(t, mountedServer(t, svc), "key-1", `{"eventId":"evt_concurrency","seatIds":["eseat_shared"]}`)
		if response.StatusCode != http.StatusBadRequest {
			response.Body.Close()
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		problem := decodeBody[problemBody](t, response)
		if problem.Code != "invalid_input" {
			t.Fatalf("problem code = %q, want invalid_input", problem.Code)
		}
		if svc.calls != 0 {
			t.Fatalf("unparsable seat id reached the service %d times", svc.calls)
		}
	})

	t.Run("missing idempotency key is forwarded for the service to reject", func(t *testing.T) {
		svc := &fakeHoldService{err: fmt.Errorf("%w: Idempotency-Key header is required", reservation.ErrInvalidInput)}
		response := postHold(t, mountedServer(t, svc), "", `{"eventId":"evt-1","seatIds":[21]}`)
		if response.StatusCode != http.StatusBadRequest {
			response.Body.Close()
			t.Fatalf("status = %d, want 400", response.StatusCode)
		}
		problem := decodeBody[problemBody](t, response)
		if problem.Code != "invalid_input" || !strings.Contains(problem.Detail, "Idempotency-Key") {
			t.Fatalf("problem = %+v, want invalid_input naming the Idempotency-Key header", problem)
		}
		if svc.key != "" || svc.eventID != "evt-1" || len(svc.seatIDs) != 1 || svc.seatIDs[0] != 21 {
			t.Fatalf("forwarded key %q event %q seats %v, want an empty key and the decoded payload", svc.key, svc.eventID, svc.seatIDs)
		}
	})

	t.Run("numeric seat identifiers are accepted", func(t *testing.T) {
		svc := &fakeHoldService{hold: reservation.Hold{ID: "hold-1", EventID: "evt-1", SeatIDs: []int64{7, 8}, Status: 201}}
		response := postHold(t, mountedServer(t, svc), "key-2", `{"eventId":"evt-1","seatIds":[7,8]}`)
		if response.StatusCode != http.StatusCreated {
			response.Body.Close()
			t.Fatalf("status = %d, want 201", response.StatusCode)
		}
		response.Body.Close()
		if len(svc.seatIDs) != 2 || svc.seatIDs[0] != 7 || svc.seatIDs[1] != 8 {
			t.Fatalf("forwarded seats %v, want [7 8]", svc.seatIDs)
		}
	})
}

type availabilityBody struct {
	EventID string `json:"eventId"`
	Seats   []struct {
		ID           int64  `json:"id"`
		LayoutSeatID string `json:"layoutSeatId"`
		State        string `json:"state"`
		Held         bool   `json:"held"`
	} `json:"seats"`
}

// Behaviour 4 (S10): the availability read reports every seat of the event with
// its held flag, an empty list instead of null, and maps its errors.
func TestListEventSeatsReportsAvailability(t *testing.T) {
	svc := &fakeHoldService{seats: []reservation.SeatAvailability{
		{ID: 21, LayoutSeatID: "seat-a", State: "available", Held: true},
		{ID: 22, LayoutSeatID: "seat-b", State: "available", Held: false},
	}}
	ts := mountedServer(t, svc)

	response, err := http.Get(ts.URL + "/api/v1/events/evt-1/seats")
	if err != nil {
		t.Fatalf("GET availability: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	body := decodeBody[availabilityBody](t, response)
	if body.EventID != "evt-1" || len(body.Seats) != 2 {
		t.Fatalf("availability body = %+v, want two seats of evt-1", body)
	}
	if !body.Seats[0].Held || body.Seats[1].Held {
		t.Fatalf("availability = %+v, want the held flag of each seat", body.Seats)
	}
	if body.Seats[0].LayoutSeatID != "seat-a" || body.Seats[0].State != "available" {
		t.Fatalf("seat = %+v, want its layout seat id and state", body.Seats[0])
	}

	svc.seats = nil
	empty, err := http.Get(ts.URL + "/api/v1/events/evt-2/seats")
	if err != nil {
		t.Fatalf("GET availability of an empty event: %v", err)
	}
	if empty.StatusCode != http.StatusOK {
		empty.Body.Close()
		t.Fatalf("status = %d, want 200", empty.StatusCode)
	}
	raw, err := io.ReadAll(empty.Body)
	empty.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(raw), `"seats":[]`) {
		t.Fatalf("empty availability body = %s, want an empty seat list", raw)
	}
}

func TestListEventSeatsMapsErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"unknown event", reservation.ErrEventNotFound, http.StatusNotFound, "not_found"},
		{"invalid event id", fmt.Errorf("%w: eventId must not be empty", reservation.ErrInvalidInput), http.StatusBadRequest, "invalid_input"},
		{"database unavailable", fmt.Errorf("%w: read tcp: connection reset", reservation.ErrUnavailable), http.StatusServiceUnavailable, "coordination_unavailable"},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ts := mountedServer(t, &fakeHoldService{seatErr: testCase.err})
			response, err := http.Get(ts.URL + "/api/v1/events/evt-1/seats")
			if err != nil {
				t.Fatalf("GET availability: %v", err)
			}
			if response.StatusCode != testCase.wantStatus {
				response.Body.Close()
				t.Fatalf("status = %d, want %d", response.StatusCode, testCase.wantStatus)
			}
			problem := decodeBody[problemBody](t, response)
			if problem.Code != testCase.wantCode {
				t.Fatalf("problem code = %q, want %q", problem.Code, testCase.wantCode)
			}
		})
	}
}
