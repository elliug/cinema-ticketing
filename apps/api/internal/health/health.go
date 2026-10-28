// Package health implements liveness and dependency-aware readiness reporting
// for the platform module.
//
// M0 foundation only. Readiness evaluates a fixed set of small Probe
// interfaces; no real PostgreSQL or Redis clients are wired in. Probes are
// injected by the caller, keeping this layer dependency-free.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
)

// Status and result identifiers.
const (
	StatusReady    = "ready"
	StatusNotReady = "not_ready"
	StatusLive     = "live"

	ResultOK          = "ok"
	ResultDegraded    = "degraded"
	ResultUnavailable = "unavailable"
)

// Result is the outcome of a single probe.
type Result struct {
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

// Healthy reports whether the result should count as a passing readiness probe.
// Only "ok" is healthy; "degraded" and "unavailable" fail readiness.
func (r Result) Healthy() bool { return r.Status == ResultOK }

// OK, Degraded, and Unavailable construct common results.
func OK() Result { return Result{Status: ResultOK} }
func Degraded(message string) Result {
	return Result{Status: ResultDegraded, Message: message}
}
func Unavailable(message string) Result {
	return Result{Status: ResultUnavailable, Message: message}
}

// Probe is a small dependency check. Implementations must be idempotent and
// cheap.
type Probe interface {
	// Name is the stable identifier of the dependency (e.g. "postgresql").
	Name() string
	// Check reports the current dependency state within the request context.
	Check(ctx context.Context) Result
}

// StaticProbe returns a fixed outcome; used for M0 wiring and tests.
type StaticProbe struct {
	// ID is the dependency identifier (e.g. "postgresql").
	ID      string
	Outcome Result
}

// Name implements Probe.
func (p StaticProbe) Name() string { return p.ID }

// Check implements Probe.
func (p StaticProbe) Check(context.Context) Result { return p.Outcome }

// ProcessProbe reports process liveness and always reports ok for readiness.
type ProcessProbe struct{}

// Name implements Probe.
func (ProcessProbe) Name() string { return "process" }

// Check implements Probe.
func (ProcessProbe) Check(context.Context) Result { return OK() }

// Service answers liveness and readiness for a fixed set of probes.
type Service struct {
	probes []Probe
}

// New builds a Service from the given probes, preserving call order.
func New(probes ...Probe) *Service { return &Service{probes: probes} }

// LiveReport is the liveness payload.
type LiveReport struct {
	Status string `json:"status"`
}

// Live reports that the process can serve and is healthy. It does not consult
// any dependency.
func (s *Service) Live(context.Context) LiveReport {
	return LiveReport{Status: StatusLive}
}

// CheckReport is the readiness payload.
type CheckReport struct {
	Status string            `json:"status"`
	Checks map[string]Result `json:"checks"`
	Errors []string          `json:"errors,omitempty"`
}

// Ready evaluates every probe and aggregates a readiness report. If all probes
// are healthy the status is "ready"; otherwise it is "not_ready" with errors
// describing each failing probe, sorted for deterministic output.
func (s *Service) Ready(ctx context.Context) CheckReport {
	report := CheckReport{Checks: make(map[string]Result, len(s.probes))}

	for _, probe := range s.probes {
		res := probe.Check(ctx)
		report.Checks[probe.Name()] = res
		if !res.Healthy() {
			msg := res.Status
			if res.Message != "" {
				msg = res.Message
			}
			report.Errors = append(report.Errors, probe.Name()+": "+msg)
		}
	}

	if len(report.Errors) == 0 {
		report.Status = StatusReady
	} else {
		report.Status = StatusNotReady
		sort.Strings(report.Errors)
	}
	return report
}

// ServeLive writes the liveness payload (HTTP 200) to the response.
func (s *Service) ServeLive(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.Live(r.Context()))
}

// ServeReady writes the readiness payload: HTTP 200 when ready, HTTP 503 when
// not.
func (s *Service) ServeReady(w http.ResponseWriter, r *http.Request) {
	report := s.Ready(r.Context())
	code := http.StatusOK
	if report.Status == StatusNotReady {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, report)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
