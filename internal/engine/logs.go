package engine

import (
	"context"
	"path/filepath"

	"github.com/williamokano/kairos/internal/artifact"
	"github.com/williamokano/kairos/internal/domain"
)

// inlineThreshold is 06-durability.md's payload-discipline line: "push
// anything over 8 KiB (diffs, transcripts, large node outputs) into the
// artifact store with a reference." A body under this size stays inlined
// in the event, exactly as before L09.
const inlineThreshold = 8 * 1024

// storeOutput decides whether body is small enough to inline in
// NodeOutputReceived.Output or must be pushed to the artifact store and
// referenced instead — the two fields are mutually exclusive (see
// domain.NodeOutputReceived's doc comment).
func (e *Engine) storeOutput(body []byte) (inline []byte, ref *domain.ArtifactRef, err error) {
	if len(body) < inlineThreshold {
		return body, nil, nil
	}
	r, err := e.artifacts.PutBytes(body)
	if err != nil {
		return nil, nil, err
	}
	return nil, &domain.ArtifactRef{Hash: r.Hash, Size: r.Size}, nil
}

// collectLogs rotates/compresses a completed node execution's stdout.log
// and stderr.log (artifact.CollectLog — a no-op below the 64 MiB
// threshold), recording LogDegraded rather than silently dropping the
// failure if either one cannot be rotated (06-durability.md: "block
// first, degrade second, never silently"). Called once per execution,
// after the process has exited — never while it may still be writing
// (see CollectLog's own doc comment on why).
func (e *Engine) collectLogs(ctx context.Context, runID, nodeID, execID, dir string) {
	for _, stream := range []string{"stdout", "stderr"} {
		path := filepath.Join(dir, stream+".log")
		if _, err := artifact.CollectLog(path); err != nil {
			if appendErr := e.appendNext(ctx, runID, domain.LogDegraded{
				RunID: runID, NodeID: nodeID, ExecID: execID,
				Stream: stream, Reason: err.Error(),
			}); appendErr != nil {
				e.log.Error("recording log.degraded", "error", appendErr)
			}
		}
	}
}
