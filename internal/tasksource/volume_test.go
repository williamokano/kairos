package tasksource_test

import (
	"testing"
	"time"

	"github.com/williamokano/kairos/internal/tasksource"
)

func TestVolumeController_passthroughFlushesImmediately(t *testing.T) {
	out := make(chan tasksource.Flush, 4)
	vc := tasksource.NewVolumeController(tasksource.VolumeConfig{}, out)
	vc.Add(tasksource.WorkItem{ID: "1", DedupeKey: "d1"}, 1)

	select {
	case f := <-out:
		if f.Digest || len(f.Items) != 1 {
			t.Errorf("flush = %+v, want one non-digest item", f)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for passthrough flush")
	}
}

func TestVolumeController_debounceCollapsesABurst(t *testing.T) {
	out := make(chan tasksource.Flush, 4)
	vc := tasksource.NewVolumeController(tasksource.VolumeConfig{Debounce: 100 * time.Millisecond}, out)

	for i := 0; i < 5; i++ {
		vc.Add(tasksource.WorkItem{ID: string(rune('a' + i)), DedupeKey: "d"}, 5)
		time.Sleep(10 * time.Millisecond)
	}

	select {
	case f := <-out:
		if len(f.Items) != 5 {
			t.Errorf("len(f.Items) = %d, want 5 (one collapsed flush)", len(f.Items))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for debounced flush")
	}
	select {
	case f := <-out:
		t.Errorf("unexpected second flush: %+v", f)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestVolumeController_batchDigestModeProducesOneDigestFlush(t *testing.T) {
	out := make(chan tasksource.Flush, 4)
	vc := tasksource.NewVolumeController(tasksource.VolumeConfig{
		Batch: tasksource.BatchConfig{Mode: "digest", MaxItems: 3},
	}, out)

	for i := 0; i < 3; i++ {
		vc.Add(tasksource.WorkItem{ID: string(rune('a' + i)), DedupeKey: "d"}, 3)
	}

	select {
	case f := <-out:
		if !f.Digest || len(f.Items) != 3 {
			t.Errorf("flush = %+v, want a digest of 3 items", f)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for batch flush")
	}
}

func TestVolumeController_degradeToBatchForcesDigestOnHighRate(t *testing.T) {
	// With no Debounce/Batch configured, the first items (before the
	// rate crosses ItemsPerMinute) flush immediately, one per item —
	// exactly like passthrough mode. Once degraded, later Adds land
	// within the same short coalescing window and flush together as a
	// digest. This test's claim is narrow and real: SOME flush after
	// degrading is a digest, not that every flush is.
	out := make(chan tasksource.Flush, 8)
	vc := tasksource.NewVolumeController(tasksource.VolumeConfig{
		DegradeToBatch: tasksource.DegradeConfig{ItemsPerMinute: 2},
	}, out)

	for i := 0; i < 6; i++ {
		vc.Add(tasksource.WorkItem{ID: string(rune('a' + i)), DedupeKey: "d"}, 6)
	}

	deadline := time.After(2 * time.Second)
	sawDigest := false
	for !sawDigest {
		select {
		case f := <-out:
			if f.Digest {
				sawDigest = true
			}
		case <-deadline:
			t.Fatal("timed out waiting for a degraded (digest) flush")
		}
	}
}
