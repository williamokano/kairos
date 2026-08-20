package tasksource

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/oklog/ulid/v2"

	"github.com/williamokano/kairos/internal/eventstore"
)

// InboxConfig configures the `~/.kairos/inbox/*.md` watcher.
type InboxConfig struct {
	Dir            string
	DefaultFlow    string
	DefaultProject string
	// QuietPeriod is how long a file must sit unmodified before pickup —
	// 2s, per 08-triggers.md, so an editor mid-save is never picked up
	// half-written.
	QuietPeriod time.Duration
	// PollFallback re-scans Dir on this cadence in addition to fsnotify —
	// "fsnotify is unreliable across editors' atomic-save dances and over
	// network mounts." 5s per the doc.
	PollFallback time.Duration
	Limits       QueueLimits
	Log          *slog.Logger
}

const inboxSourceID = "inbox"

// RunInbox watches cfg.Dir until ctx is cancelled, turning each stable
// *.md file into exactly one trigger via TriggerRun. It never returns an
// error for a single bad file — a malformed drop moves to .failed/ with a
// sibling .err.json, per the doc, and the watcher keeps running.
func RunInbox(ctx context.Context, cfg InboxConfig, store eventstore.Store) error {
	log := cfg.Log
	if log == nil {
		log = slog.Default()
	}
	if cfg.QuietPeriod <= 0 {
		cfg.QuietPeriod = 2 * time.Second
	}
	if cfg.PollFallback <= 0 {
		cfg.PollFallback = 5 * time.Second
	}
	for _, sub := range []string{"", ".taken", ".dup", ".failed"} {
		if err := os.MkdirAll(filepath.Join(cfg.Dir, sub), 0o700); err != nil {
			return fmt.Errorf("creating inbox dir %s: %w", filepath.Join(cfg.Dir, sub), err)
		}
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("creating inbox watcher: %w", err)
	}
	defer func() { _ = watcher.Close() }()
	if err := watcher.Add(cfg.Dir); err != nil {
		return fmt.Errorf("watching inbox dir: %w", err)
	}

	ib := &inboxWatcher{cfg: cfg, store: store, log: log, pending: map[string]*time.Timer{}}

	pollTicker := time.NewTicker(cfg.PollFallback)
	defer pollTicker.Stop()

	ib.scan()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-watcher.Events:
			if !ok {
				return nil
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write) != 0 {
				ib.noteCandidate(ev.Name)
			}
		case <-watcher.Errors:
			// A watcher error is not this file's fault; the poll
			// fallback keeps correctness even if fsnotify degrades.
		case <-pollTicker.C:
			ib.scan()
		}
	}
}

type inboxWatcher struct {
	cfg   InboxConfig
	store eventstore.Store
	log   *slog.Logger

	mu      sync.Mutex
	pending map[string]*time.Timer
}

func (ib *inboxWatcher) scan() {
	entries, err := os.ReadDir(ib.cfg.Dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		ib.noteCandidate(filepath.Join(ib.cfg.Dir, e.Name()))
	}
}

// noteCandidate (re)starts path's quiet-period timer — "a file is picked
// up once it has been unmodified for 2s."
func (ib *inboxWatcher) noteCandidate(path string) {
	ib.mu.Lock()
	defer ib.mu.Unlock()
	if t, ok := ib.pending[path]; ok {
		t.Stop()
	}
	ib.pending[path] = time.AfterFunc(ib.cfg.QuietPeriod, func() {
		ib.mu.Lock()
		delete(ib.pending, path)
		ib.mu.Unlock()
		ib.pickup(path)
	})
}

// pickup implements the doc's exact mechanism: `rename(inbox/x.md,
// inbox/.taken/<id>-x.md)` first — the atomic rename is what stops
// fsnotify/poll-fallback double-processing the same file — then
// dedupe-by-content-hash via TriggerRun, which is a second, independent
// safety net against identical content dropped twice.
func (ib *inboxWatcher) pickup(path string) {
	content, err := os.ReadFile(path)
	if err != nil {
		return // already moved by a concurrent pickup, or gone — not an error
	}

	pickupID := ulid.Make().String()
	takenPath := filepath.Join(ib.cfg.Dir, ".taken", pickupID+"-"+filepath.Base(path))
	if err := os.Rename(path, takenPath); err != nil {
		return // lost the race to another pickup of the same file
	}

	frontMatter, body := splitFrontMatter(content)
	dedupeKey := "inbox:" + sha256Hex(content)

	flow := frontMatter["flow"]
	if flow == "" {
		flow = ib.cfg.DefaultFlow
	}
	params, _ := json.Marshal(map[string]any{
		"body":     body,
		"project":  orDefault(frontMatter["project"], ib.cfg.DefaultProject),
		"priority": frontMatter["priority"],
		"budget":   frontMatter["budget"],
	})

	runID, created, err := TriggerRun(context.Background(), ib.store, dedupeKey, inboxSourceID, pickupID,
		CreateRunRequest{
			DefinitionRef: flow, Params: params,
			TriggerRef: "inbox:" + dedupeKey, Actor: "trigger:inbox",
		}, ib.cfg.Limits)
	if err != nil {
		ib.moveToFailed(takenPath, err)
		return
	}
	if !created {
		ib.moveToDup(takenPath, runID)
	}
}

func (ib *inboxWatcher) moveToFailed(path string, cause error) {
	dest := filepath.Join(ib.cfg.Dir, ".failed", filepath.Base(path))
	if err := os.Rename(path, dest); err != nil {
		ib.log.Error("tasksource: moving failed inbox item", "path", path, "err", err)
		return
	}
	errJSON, _ := json.Marshal(map[string]string{"error": cause.Error()})
	_ = os.WriteFile(dest+".err.json", errJSON, 0o600)
}

func (ib *inboxWatcher) moveToDup(path, existingRunID string) {
	dest := filepath.Join(ib.cfg.Dir, ".dup", filepath.Base(path))
	if err := os.Rename(path, dest); err != nil {
		return
	}
	note := fmt.Sprintf("duplicate of run %s\n", existingRunID)
	_ = os.WriteFile(dest+".note.txt", []byte(note), 0o600)
}

// splitFrontMatter parses `---\nkey: value\n---\nbody` — a small, literal
// subset of YAML front matter (scalar string values only), not a general
// YAML parser: the inbox's fields (flow, project, priority, budget) are
// all flat scalars in every example this document specifies.
func splitFrontMatter(content []byte) (map[string]string, string) {
	s := string(content)
	fm := map[string]string{}
	if !strings.HasPrefix(s, "---\n") {
		return fm, s
	}
	rest := s[4:]
	end := strings.Index(rest, "\n---\n")
	if end < 0 {
		return fm, s
	}
	header := rest[:end]
	body := rest[end+5:]
	for _, line := range strings.Split(header, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		fm[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	return fm, strings.TrimSpace(body)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
