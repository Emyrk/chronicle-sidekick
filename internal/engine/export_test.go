package engine

import (
	"context"
	"time"

	"github.com/Emyrk/chronicle-sidekick/internal/store"
)

// Step exposes a single poll to tests.
func (e *Engine) Step(ctx context.Context, now time.Time, manual bool) { e.step(ctx, now, manual) }

// StepFor exposes a selected-install manual poll to tests.
func (e *Engine) StepFor(ctx context.Context, now time.Time, dirs []string) {
	e.stepSelected(ctx, now, &uploadRequest{dirs: dirs})
}

// SelectAppendGroup exposes restart grouping to tests.
func SelectAppendGroup(groups map[string]store.ArchiveGroup, installDir string, startedAt time.Time) (store.ArchiveGroup, bool) {
	return selectAppendGroup(groups, installDir, startedAt)
}

// ArchiveCombatPath exposes deterministic archive naming to tests.
func ArchiveCombatPath(dir string, now time.Time) string { return archiveCombatPath(dir, now) }

// Busy reports whether the worker has uploads queued or in flight.
func (e *Engine) Busy() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.busy()
}
