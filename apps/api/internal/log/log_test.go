package log_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cinema.local/ticketing/apps/api/internal/log"
)

func TestNewValidLevelWritesJSON(t *testing.T) {
	var buf bytes.Buffer
	logger, err := log.New("info", &buf)
	if err != nil {
		t.Fatalf("New returned error: %v", err)
	}
	if logger == nil {
		t.Fatal("New returned nil logger")
	}

	logger.Info("server started", "addr", ":8080")

	line := buf.String()
	if !strings.Contains(line, `"msg":"server started"`) {
		t.Errorf("log line missing msg field: %q", line)
	}
	if !strings.Contains(line, `"level":"INFO"`) {
		t.Errorf("log line missing uppercased level: %q", line)
	}
	// The output must be a single structured JSON object.
	recorded := strings.TrimRight(line, "\n")
	var record map[string]any
	if err := json.Unmarshal([]byte(recorded), &record); err != nil {
		t.Fatalf("log output is not valid JSON: %v (%s)", err, recorded)
	}
}

func TestNewInvalidLevel(t *testing.T) {
	var buf bytes.Buffer
	if _, err := log.New("debugger", &buf); err == nil {
		t.Fatal("expected error for invalid log level, got nil")
	}
}
