package local

import (
	"context"
	"errors"
	"syscall"
	"time"
)

// Cancel runs the cancellation sequence 01-architecture.md specifies, no
// steps skipped: SIGTERM -> wait killGrace -> SIGKILL. (Closing stdin and
// draining log files for two more seconds are engine/reconciliation-level
// concerns once output collection exists; this method covers the signal
// half only, which is all L05's milestone needs.)
func (l *Local) Cancel(ctx context.Context, pgid int, killGrace time.Duration) error {
	if err := l.Signal(ctx, pgid, SignalTerm); err != nil {
		return err
	}

	timer := time.NewTimer(killGrace)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}

	// SIGKILL unconditionally: if the group already exited from SIGTERM,
	// Kill on an empty group is a harmless ESRCH — swallowed here, since
	// it means Cancel achieved its goal (the group is gone) by an
	// alternate, equally acceptable route.
	if err := l.Signal(ctx, pgid, SignalKill); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
