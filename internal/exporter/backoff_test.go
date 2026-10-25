package exporter

import (
	"testing"
	"time"
)

func TestBackoffSequenceCapsAtTwentyMinutes(t *testing.T) {
	var b backoff
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if !b.Allowed(now) {
		t.Fatal("fresh backoff should allow the first attempt")
	}

	for i, want := range []time.Duration{5 * time.Minute, 10 * time.Minute, 20 * time.Minute, 20 * time.Minute, 20 * time.Minute} {
		b.Failure(now)
		if b.Allowed(now.Add(want - time.Second)) {
			t.Errorf("failure %d: should still be blocked just before %v", i+1, want)
		}
		if !b.Allowed(now.Add(want)) {
			t.Errorf("failure %d: should be allowed at exactly %v", i+1, want)
		}
		now = now.Add(want)
	}
}

func TestBackoffSuccessResets(t *testing.T) {
	var b backoff
	now := time.Now()
	b.Failure(now)
	b.Failure(now)
	b.Success()
	if !b.Allowed(now) {
		t.Fatal("success should clear the wait")
	}
	b.Failure(now)
	if b.Allowed(now.Add(5*time.Minute - time.Second)) {
		t.Error("after a reset the next wait should be back to 5m")
	}
	if !b.Allowed(now.Add(5 * time.Minute)) {
		t.Error("after a reset the next wait should be exactly 5m")
	}
}
