//go:build acceptance_m2

// Package concurrency_test holds the accepted M2 concurrency contract: N parallel
// hold requests for the same seat selection yield at most one successful
// reservation and no partial selection success.
//
// The contract drives a real server, so it needs a seeded event. It never invents
// identifiers: the event and the contended event seats come from the environment,
// because the hold API validates its input and rejects a fixture id it could not
// have stored.
package concurrency_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// contestedSeats is the number of parallel contenders: one winner and fifteen
// conflicts is the accepted contract.
const contestedSeats = 16

// holdRequest is the wire shape of a hold, matching publicapi.createHoldRequest.
type holdRequest struct {
	EventID string  `json:"eventId"`
	SeatIDs []int64 `json:"seatIds"`
}

// holdOutcome is what one contender observed: the status, and for a conflict the
// problem code, for a success the created hold.
type holdOutcome struct {
	contender int
	status    int
	code      string
	holdID    string
	expiresAt time.Time
}

type problemBody struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

type holdBody struct {
	ID        string    `json:"id"`
	EventID   string    `json:"eventId"`
	SeatIDs   []int64   `json:"seatIds"`
	ExpiresAt time.Time `json:"expiresAt"`
}

// TestOverlappingHoldsAllowExactlyOneWinner is the accepted S7/S8 contract: 16
// contenders race for the same seat selection, each with its own
// Idempotency-Key, and exactly one 201 with fifteen 409 responses come back.
//
// Required environment:
//
//	M2_API_URL    base URL of the running API, for example http://127.0.0.1:18081
//	M2_EVENT_ID   id of a seeded event with available seats
//	M2_SEAT_IDS   the contended event seat ids, comma separated (real bigint ids)
func TestOverlappingHoldsAllowExactlyOneWinner(t *testing.T) {
	baseURL := strings.TrimSuffix(strings.TrimSpace(os.Getenv("M2_API_URL")), "/")
	var missing []string
	for _, name := range []string{"M2_API_URL", "M2_EVENT_ID", "M2_SEAT_IDS"} {
		if strings.TrimSpace(os.Getenv(name)) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("the M2 concurrency contract drives a real server and needs the seeded identifiers: set %s "+
			"(M2_API_URL is the base URL of the running API; M2_EVENT_ID and M2_SEAT_IDS are the event id and the "+
			"comma separated event seat ids of a seeded, published event)", strings.Join(missing, ", "))
	}

	eventID := strings.TrimSpace(os.Getenv("M2_EVENT_ID"))
	seatIDs := parseSeatIDs(t, os.Getenv("M2_SEAT_IDS"))
	payload, err := json.Marshal(holdRequest{EventID: eventID, SeatIDs: seatIDs})
	if err != nil {
		t.Fatalf("encode the hold payload: %v", err)
	}

	keys := make([]string, contestedSeats)
	for contender := range keys {
		// One distinct key per contender: the contract is about seat contention,
		// not about idempotent replay.
		keys[contender] = fmt.Sprintf("m2-concurrency-%02d-%d", contender, time.Now().UnixNano())
	}

	start := make(chan struct{})
	outcomes := make(chan holdOutcome, contestedSeats)
	client := &http.Client{Timeout: 15 * time.Second}

	var group sync.WaitGroup
	for contender := 0; contender < contestedSeats; contender++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start

			request, err := http.NewRequestWithContext(
				context.Background(),
				http.MethodPost,
				baseURL+"/api/v1/reservations/holds",
				bytes.NewReader(payload),
			)
			if err != nil {
				outcomes <- holdOutcome{contender: index, status: -1, code: "build request: " + err.Error()}
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", keys[index])

			response, err := client.Do(request)
			if err != nil {
				outcomes <- holdOutcome{contender: index, status: -2, code: "request failed: " + err.Error()}
				return
			}
			defer response.Body.Close()
			raw, err := io.ReadAll(response.Body)
			if err != nil {
				outcomes <- holdOutcome{contender: index, status: -3, code: "read body: " + err.Error()}
				return
			}
			outcome := holdOutcome{contender: index, status: response.StatusCode}
			switch response.StatusCode {
			case http.StatusCreated:
				var hold holdBody
				if err := json.Unmarshal(raw, &hold); err != nil {
					outcome.code = "decode created hold: " + err.Error()
					break
				}
				outcome.holdID, outcome.expiresAt = hold.ID, hold.ExpiresAt
			default:
				var problem problemBody
				if err := json.Unmarshal(raw, &problem); err != nil {
					outcome.code = "decode problem: " + err.Error()
					break
				}
				outcome.code = problem.Code
			}
			outcomes <- outcome
		}(contender)
	}

	close(start)
	group.Wait()
	close(outcomes)

	seen := map[string]struct{}{}
	for _, key := range keys {
		if _, duplicate := seen[key]; duplicate {
			t.Fatalf("contenders reused the Idempotency-Key %q, want one distinct key each", key)
		}
		seen[key] = struct{}{}
	}

	counts := map[int]int{}
	var winners []holdOutcome
	for outcome := range outcomes {
		counts[outcome.status]++
		switch outcome.status {
		case http.StatusCreated:
			if outcome.holdID == "" {
				t.Fatalf("contender %d was the winner but returned no hold id", outcome.contender)
			}
			if outcome.expiresAt.IsZero() || !outcome.expiresAt.After(time.Now()) {
				t.Fatalf("contender %d won with expiresAt %s, want a future hold deadline", outcome.contender, outcome.expiresAt)
			}
			winners = append(winners, outcome)
		case http.StatusConflict:
			if outcome.code != "seat_unavailable" {
				t.Fatalf("contender %d lost with problem code %q, want seat_unavailable", outcome.contender, outcome.code)
			}
		default:
			t.Fatalf("contender %d reported status %d with problem code %q, want 201 or 409",
				outcome.contender, outcome.status, outcome.code)
		}
	}

	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != contestedSeats-1 {
		encoded, _ := json.Marshal(counts)
		t.Fatalf("expected exactly one 201 and %d conflicts; got %s", contestedSeats-1, encoded)
	}
	if len(winners) != 1 {
		t.Fatalf("winners = %d, want exactly one successful reservation", len(winners))
	}
	t.Logf("%d contenders: %d created (hold %s), %d conflicts with seat_unavailable, %d distinct idempotency keys",
		contestedSeats, counts[http.StatusCreated], winners[0].holdID, counts[http.StatusConflict], len(seen))

	// S10 evidence: availability reports the winner's seats as held, so the seat
	// selection UI can refetch after a conflict.
	held := heldSeats(t, client, baseURL, eventID)
	for _, seatID := range seatIDs {
		if !held[seatID] {
			t.Fatalf("availability reports contended seat %d as free after a winner holds it: %v", seatID, held)
		}
	}
}

// parseSeatIDs reads the contended event seat ids from M2_SEAT_IDS. The ids are
// real bigint identifiers, so an empty or non-numeric list is a configuration
// error rather than a silently skipped seat.
func parseSeatIDs(t *testing.T, raw string) []int64 {
	t.Helper()
	parts := strings.Split(raw, ",")
	seats := make([]int64, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		seatID, err := strconv.ParseInt(trimmed, 10, 64)
		if err != nil {
			t.Fatalf("M2_SEAT_IDS entry %q is not an event seat id: %v", trimmed, err)
		}
		if seatID <= 0 {
			t.Fatalf("M2_SEAT_IDS entry %q must be a positive event seat id", trimmed)
		}
		seats = append(seats, seatID)
	}
	if len(seats) == 0 {
		t.Fatal("M2_SEAT_IDS must list at least one event seat id")
	}
	return seats
}

// heldSeats reads the public availability of one event and reports, per event
// seat id, whether a live hold owns it.
func heldSeats(t *testing.T, client *http.Client, baseURL, eventID string) map[int64]bool {
	t.Helper()
	response, err := client.Get(baseURL + "/api/v1/events/" + eventID + "/seats")
	if err != nil {
		t.Fatalf("GET availability of %s: %v", eventID, err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(response.Body)
		t.Fatalf("availability for %s returned %d: %s", eventID, response.StatusCode, raw)
	}
	var body struct {
		Seats []struct {
			ID   int64 `json:"id"`
			Held bool  `json:"held"`
		} `json:"seats"`
	}
	if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
		t.Fatalf("decode availability of %s: %v", eventID, err)
	}
	held := make(map[int64]bool, len(body.Seats))
	for _, seat := range body.Seats {
		held[seat.ID] = seat.Held
	}
	return held
}
