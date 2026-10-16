package engine

import (
	"context"
	"time"
)

// Step exposes a single poll to tests.
func (e *Engine) Step(ctx context.Context, now time.Time, manual bool) { e.step(ctx, now, manual) }

// StepFor exposes a selected-install manual poll to tests.
func (e *Engine) StepFor(ctx context.Context, now time.Time, dirs []string) {
	e.stepSelected(ctx, now, &uploadRequest{dirs: dirs})
}

// Busy reports whether the worker has uploads or archiving queued or in flight.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy() || len(e.archiveQ) > 0
}
