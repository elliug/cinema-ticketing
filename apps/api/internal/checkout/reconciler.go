// The payment reconciler: the worker that closes issue #87.
//
// #82 made the expiry sweep skip a hold whose payment has an attempt still 'pending',
// which is what stops money being captured for seats that were already released. The
// cost of that guard is that nothing ever ends such an attempt on its own: a process
// that dies during the provider call leaves the attempt pending, and its seats held,
// for ever.
//
// This worker ends them the only way that is safe: by asking the provider again. The
// provider reference is derived from the attempt identity, so it is deterministic, and
// the provider replays an existing charge instead of making a second one — which is why
// a re-call cannot double charge and can answer what really happened. The attempt stops
// being pending only when the provider has answered, so the sweep never releases a hold
// whose money question is still open.
package checkout

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// ReconcilerConfig is the process wiring of the payment reconciler.
type ReconcilerConfig struct {
	// Interval is the cadence between ticks. It must be positive.
	Interval time.Duration
	// Age is how long an attempt must have been pending before it is treated as
	// abandoned. It is the hold window: an attempt older than the hold it belongs to
	// cannot be a live provider call, because a payment cannot start on a hold whose
	// window has already closed.
	Age time.Duration
	// Batch bounds how many attempts one tick settles, so a backlog cannot monopolise
	// the worker; the next tick continues from where this one stopped.
	Batch int
	// Timeout bounds one tick, including the provider calls, so a provider that stops
	// answering cannot wedge the worker. A tick that times out leaves its attempts
	// pending, which is the state they were already in.
	Timeout time.Duration
	// Logger receives the tick reports and every failure. A failed tick is logged and
	// retried on the next one; it is never fatal to the process.
	Logger *slog.Logger
}

// Reconciler settles the payments a dead process left pending. It keeps no state
// between ticks: the durable attempt row is the whole input, so a restarted worker
// resumes from it and two workers divide the work instead of duplicating it.
type Reconciler struct {
	service *Service
	cfg     ReconcilerConfig
}

// NewReconciler builds the reconciler, refusing a configuration that could not make
// progress instead of starting a loop that spins or replays every attempt in one tick.
func NewReconciler(service *Service, cfg ReconcilerConfig) (*Reconciler, error) {
	if service == nil {
		return nil, fmt.Errorf("%w: payment reconciliation needs a checkout service", ErrInvalidInput)
	}
	if cfg.Interval <= 0 {
		return nil, fmt.Errorf("%w: reconciliation interval must be positive, got %s", ErrInvalidInput, cfg.Interval)
	}
	if cfg.Age <= 0 {
		return nil, fmt.Errorf("%w: reconciliation age must be positive, got %s", ErrInvalidInput, cfg.Age)
	}
	if cfg.Batch <= 0 {
		return nil, fmt.Errorf("%w: reconciliation batch must be positive, got %d", ErrInvalidInput, cfg.Batch)
	}
	if cfg.Timeout <= 0 {
		return nil, fmt.Errorf("%w: reconciliation timeout must be positive, got %s", ErrInvalidInput, cfg.Timeout)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	return &Reconciler{service: service, cfg: cfg}, nil
}

// Run reconciles immediately and then on every interval until the process context is
// cancelled. A failed tick is logged and the next tick tries again, so a transient
// provider or database failure never stops reconciliation.
func (r *Reconciler) Run(ctx context.Context) {
	r.cfg.Logger.Info("payment reconciliation started",
		"interval", r.cfg.Interval, "age", r.cfg.Age, "batch", r.cfg.Batch, "timeout", r.cfg.Timeout)
	defer r.cfg.Logger.Info("payment reconciliation stopped")

	ticker := time.NewTicker(r.cfg.Interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := r.Reconcile(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			r.cfg.Logger.Error("payment reconciliation failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Reconcile runs one tick: settle at most Batch attempts that have been pending for
// longer than Age, each bounded by the tick's timeout and the process context.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	tickCtx, cancel := context.WithTimeout(ctx, r.cfg.Timeout)
	defer cancel()

	result, err := r.service.ReconcileStaleAttempts(tickCtx, r.cfg.Age, r.cfg.Batch)
	if err != nil {
		return err
	}
	if result.Candidates == 0 {
		return nil
	}
	r.cfg.Logger.Info("payment reconciliation settled stale attempts",
		"candidates", result.Candidates, "captured", result.Captured,
		"declined", result.Declined, "unresolved", result.Unresolved)
	// An unresolved attempt keeps its seats on purpose: the provider did not answer, so
	// there is no verdict, and releasing the seats would be the defect #82 closed. It is
	// logged at warn level because a human may need to look if it keeps happening.
	if result.Unresolved > 0 {
		r.cfg.Logger.Warn("payment reconciliation left attempts pending: their seats stay held until the provider answers",
			"unresolved", result.Unresolved)
	}
	return nil
}
