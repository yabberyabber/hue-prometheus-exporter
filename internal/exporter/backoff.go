package exporter

import "time"

const (
	discoveryBackoffBase = 5 * time.Minute
	discoveryBackoffMax  = 20 * time.Minute
)

// backoff gates bridge discovery after failures: 5m, then 10m, then 20m,
// staying at 20m until a discovery succeeds. A success resets it, so the
// next failure starts over at 5m. Discovery is multicast traffic that
// every device on the LAN hears, so it must not be retried in a tight loop
// while the bridge is down or unplugged.
type backoff struct {
	failures int
	notUntil time.Time
}

// Allowed reports whether a discovery attempt may run at now.
func (b *backoff) Allowed(now time.Time) bool {
	return !now.Before(b.notUntil)
}

// Failure records a failed discovery attempt made at now.
func (b *backoff) Failure(now time.Time) {
	wait := discoveryBackoffBase
	for i := 0; i < b.failures && wait < discoveryBackoffMax; i++ {
		wait *= 2
	}
	if wait > discoveryBackoffMax {
		wait = discoveryBackoffMax
	}
	b.failures++
	b.notUntil = now.Add(wait)
}

// Success clears the backoff.
func (b *backoff) Success() {
	b.failures = 0
	b.notUntil = time.Time{}
}
