package download

import (
	"testing"
	"time"
)

// TestSampleSpeedBaseline proves the first progress event after a resume only
// establishes the baseline: the bytes already on disk must not be reported as
// a burst of speed.
func TestSampleSpeedBaseline(t *testing.T) {
	st := &jobState{}
	now := time.Unix(1700000000, 0)

	if got := st.sampleSpeed(now, 10<<20); got != 0 {
		t.Fatalf("baseline speed = %d, want 0", got)
	}
	if st.speedBytes != 10<<20 {
		t.Fatalf("baseline bytes = %d, want %d", st.speedBytes, 10<<20)
	}
}

// TestSampleSpeedMeasuresWindow checks a tidy arithmetic case: 512 KiB over
// half a second is 1 MiB/s.
func TestSampleSpeedMeasuresWindow(t *testing.T) {
	st := &jobState{}
	now := time.Unix(1700000000, 0)
	st.sampleSpeed(now, 0)

	got := st.sampleSpeed(now.Add(500*time.Millisecond), 512<<10)
	want := int64(1 << 20)
	if got != want {
		t.Fatalf("speed = %d, want %d", got, want)
	}
}

// TestSampleSpeedDecaysOnStall keeps the readout honest: with no new bytes the
// smoothed value must fall instead of freezing at the previous burst.
func TestSampleSpeedDecaysOnStall(t *testing.T) {
	st := &jobState{}
	now := time.Unix(1700000000, 0)
	st.sampleSpeed(now, 0)
	peak := st.sampleSpeed(now.Add(500*time.Millisecond), 4<<20) // 8 MiB/s
	if peak <= 0 {
		t.Fatalf("peak speed = %d, want > 0", peak)
	}

	// Another half second, zero bytes: instant rate 0 pulls the average down.
	quiet := st.sampleSpeed(now.Add(time.Second), 4<<20)
	if quiet >= peak {
		t.Fatalf("speed did not decay: quiet = %d, peak = %d", quiet, peak)
	}
	if quiet < 0 {
		t.Fatalf("speed went negative: %d", quiet)
	}
}

// TestSampleSpeedIgnoresShortIntervals makes sure a 150 ms poll does not
// re-sample (and so does not divide by a tiny Δt).
func TestSampleSpeedIgnoresShortIntervals(t *testing.T) {
	st := &jobState{}
	now := time.Unix(1700000000, 0)
	st.sampleSpeed(now, 0)
	first := st.sampleSpeed(now.Add(400*time.Millisecond), 1<<20)

	// 100 ms later, more bytes arrive but below the sampling window.
	again := st.sampleSpeed(now.Add(500*time.Millisecond), 2<<20)
	if again != first {
		t.Fatalf("re-sampled inside the window: first = %d, again = %d", first, again)
	}
	if st.speedBytes != 1<<20 {
		t.Fatalf("sample bytes = %d, want %d", st.speedBytes, 1<<20)
	}
}

// TestResetSpeed clears the meter so a paused job shows no stale number.
func TestResetSpeed(t *testing.T) {
	st := &jobState{}
	now := time.Unix(1700000000, 0)
	st.sampleSpeed(now, 0)
	st.sampleSpeed(now.Add(time.Second), 1<<20)
	if st.info.Speed == 0 {
		// info.Speed is set by progress, not sampleSpeed; mirror that here.
		st.info.Speed = st.sampleSpeed(now.Add(2*time.Second), 2<<20)
	}
	st.resetSpeed()
	if st.info.Speed != 0 || st.speedAvg != 0 || !st.speedAt.IsZero() || st.speedBytes != 0 {
		t.Fatalf("reset left state behind: speed=%d avg=%v at=%v bytes=%d",
			st.info.Speed, st.speedAvg, st.speedAt, st.speedBytes)
	}
}
