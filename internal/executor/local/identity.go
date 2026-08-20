package local

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// ReadProcRecord reads dir/proc.json, the confirmed identity Start wrote
// after fork/exec. A missing file means Start never got that far (the
// process is only recorded as "spawning", or nothing was ever attempted).
func ReadProcRecord(dir string) (ProcRecord, bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, "proc.json"))
	if errors.Is(err, os.ErrNotExist) {
		return ProcRecord{}, false, nil
	}
	if err != nil {
		return ProcRecord{}, false, fmt.Errorf("reading proc.json: %w", err)
	}
	var rec ProcRecord
	if err := json.Unmarshal(b, &rec); err != nil {
		return ProcRecord{}, false, fmt.Errorf("unmarshalling proc.json: %w", err)
	}
	return rec, true, nil
}

// Verdict is reconciliation's four-way outcome for a recorded process.
type Verdict int

const (
	// VerdictAlive: the pgid is running and its boot ID matches the
	// current boot — it is genuinely still the process that was started.
	VerdictAlive Verdict = iota
	// VerdictDead: no process answers at pgid; the recorded work either
	// finished (check for output.json) or was lost.
	VerdictDead
	// VerdictUnverifiable: the recorded boot ID does not match the
	// current boot. The pgid may have been reused by an unrelated
	// process since reboot — signalling it would kill a stranger's
	// process tree, so reconciliation must never call Signal for this
	// verdict (only NodeExecutionLost, no kill).
	VerdictUnverifiable
)

// Probe reconciles a recorded ProcRecord against present reality: is pgid
// alive, and if so, does its boot ID still match currentBootID?
func Probe(rec ProcRecord, currentBootID string) Verdict {
	if rec.BootID != currentBootID {
		return VerdictUnverifiable
	}
	if !ProcessGroupAlive(rec.PGID) {
		return VerdictDead
	}
	return VerdictAlive
}

// ProcessGroupAlive reports whether any process in pgid is still running,
// via the classic kill(pgid, 0) liveness probe (sends no signal, only
// checks permission/existence).
func ProcessGroupAlive(pgid int) bool {
	if pgid <= 0 {
		return false
	}
	err := syscall.Kill(-pgid, 0)
	return err == nil
}
