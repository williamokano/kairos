package tasksource

import (
	"sync"
	"time"
)

// VolumeConfig is 08-triggers.md's `volume:` block — "new core code, not
// configuration over existing machinery." A zero VolumeConfig means
// passthrough: every item flushes immediately, alone (the historical
// behaviour, correct for low-volume sources like an issue tracker where
// one item genuinely deserves one run).
type VolumeConfig struct {
	// Debounce coalesces a burst before flushing anything. Zero disables
	// debouncing (flush immediately on Add).
	Debounce time.Duration
	// Batch, if Mode != "", turns a flush into one digest run over every
	// item collected instead of one run per item.
	Batch BatchConfig
	// DegradeToBatch forces batch mode once volume crosses a threshold,
	// even for a source normally configured for per-item runs —
	// "a flood is Tuesday, not a misconfiguration."
	DegradeToBatch DegradeConfig
}

type BatchConfig struct {
	Mode     string // "digest" — the only mode this document implements
	MaxItems int
	Window   time.Duration
}

type DegradeConfig struct {
	ItemsPerMinute int
	QueueDepth     int
}

// Flush is one VolumeController output: either Digest is true and Items
// holds every item collected since the last flush (one run should be
// created over all of them), or Digest is false and Items holds exactly
// one item (one run per item, the default).
type Flush struct {
	Digest bool
	Items  []WorkItem
}

// VolumeController is the real debounce/batch/degrade state machine
// 08-triggers.md requires — not a config field that does nothing. One
// instance per source; Add is safe for concurrent use, though in
// practice only the source's own poller/inbox goroutine calls it.
type VolumeController struct {
	cfg VolumeConfig
	out chan<- Flush

	mu           sync.Mutex
	pending      []WorkItem
	debounceT    *time.Timer
	windowT      *time.Timer
	recentTimes  []time.Time // for the itemsPerMinute rate window
	forcedDigest bool
}

// NewVolumeController returns a controller that sends each Flush to out.
// The caller owns out and must keep draining it.
func NewVolumeController(cfg VolumeConfig, out chan<- Flush) *VolumeController {
	return &VolumeController{cfg: cfg, out: out}
}

// Add enqueues one item, applying debounce/batch/degrade rules, and
// flushes (synchronously, on this goroutine, via a background timer
// firing on its own goroutine) once the debounce window is quiet or the
// batch window/size caps are hit.
func (v *VolumeController) Add(item WorkItem, queueDepth int) {
	v.mu.Lock()
	defer v.mu.Unlock()

	v.pending = append(v.pending, item)
	v.recentTimes = append(v.recentTimes, time.Now())
	v.pruneRate()

	if v.shouldDegradeLocked(queueDepth) {
		v.forcedDigest = true
	}

	batching := v.forcedDigest || v.cfg.Batch.Mode == "digest"

	if !batching && v.cfg.Debounce == 0 {
		// Passthrough: no debounce, no batching — flush this one item now.
		v.flushLocked()
		return
	}

	if batching && v.cfg.Batch.MaxItems > 0 && len(v.pending) >= v.cfg.Batch.MaxItems {
		v.flushLocked()
		return
	}

	if batching && v.cfg.Batch.Window > 0 && v.windowT == nil {
		v.windowT = time.AfterFunc(v.cfg.Batch.Window, func() {
			v.mu.Lock()
			defer v.mu.Unlock()
			v.flushLocked()
		})
	}

	debounce := v.cfg.Debounce
	if debounce == 0 {
		debounce = 20 * time.Millisecond // batching with no explicit debounce still coalesces same-tick arrivals
	}
	if v.debounceT != nil {
		v.debounceT.Stop()
	}
	v.debounceT = time.AfterFunc(debounce, func() {
		v.mu.Lock()
		defer v.mu.Unlock()
		v.flushLocked()
	})
}

// shouldDegradeLocked reports whether current volume crosses
// DegradeToBatch's thresholds. Caller holds v.mu.
func (v *VolumeController) shouldDegradeLocked(queueDepth int) bool {
	d := v.cfg.DegradeToBatch
	if d.ItemsPerMinute == 0 && d.QueueDepth == 0 {
		return false
	}
	if d.ItemsPerMinute > 0 && len(v.recentTimes) > d.ItemsPerMinute {
		return true
	}
	if d.QueueDepth > 0 && queueDepth >= d.QueueDepth {
		return true
	}
	return false
}

func (v *VolumeController) pruneRate() {
	cutoff := time.Now().Add(-time.Minute)
	i := 0
	for ; i < len(v.recentTimes); i++ {
		if v.recentTimes[i].After(cutoff) {
			break
		}
	}
	v.recentTimes = v.recentTimes[i:]
}

// flushLocked emits the pending batch and resets state. Caller holds v.mu.
func (v *VolumeController) flushLocked() {
	if len(v.pending) == 0 {
		return
	}
	if v.debounceT != nil {
		v.debounceT.Stop()
		v.debounceT = nil
	}
	if v.windowT != nil {
		v.windowT.Stop()
		v.windowT = nil
	}
	items := v.pending
	v.pending = nil
	digest := v.forcedDigest || v.cfg.Batch.Mode == "digest"
	v.out <- Flush{Digest: digest, Items: items}
}
