//go:build acceptance_m2

package concurrency_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"
)

func TestOverlappingHoldsAllowExactlyOneWinner(t *testing.T) {
	baseURL := os.Getenv("M2_API_URL")
	if baseURL == "" {
		t.Fatal("M2_API_URL is required: the reservation hold API is not implemented in M0")
	}

	const contenders = 16
	payload := []byte(`{"eventId":"evt_concurrency","seatIds":["eseat_shared"]}`)
	start := make(chan struct{})
	results := make(chan int, contenders)
	errors := make(chan error, contenders)

	client := &http.Client{Timeout: 5 * time.Second}
	var group sync.WaitGroup
	for contender := 0; contender < contenders; contender++ {
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
				errors <- err
				return
			}
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Idempotency-Key", fmt.Sprintf("m2-concurrency-%02d", index))

			response, err := client.Do(request)
			if err != nil {
				errors <- err
				return
			}
			defer response.Body.Close()
			if _, err := io.Copy(io.Discard, response.Body); err != nil {
				errors <- err
				return
			}
			results <- response.StatusCode
		}(contender)
	}

	close(start)
	group.Wait()
	close(results)
	close(errors)

	for err := range errors {
		if err != nil {
			t.Fatalf("concurrent hold request failed: %v", err)
		}
	}

	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[http.StatusCreated] != 1 || counts[http.StatusConflict] != contenders-1 {
		encoded, _ := json.Marshal(counts)
		t.Fatalf("expected exactly one 201 and %d conflicts; got %s", contenders-1, encoded)
	}
}
