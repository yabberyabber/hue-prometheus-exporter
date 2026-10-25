package exporter

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

type fakeDisc struct {
	calls int
	addr  string
	err   error
}

func (f *fakeDisc) Discover(context.Context, string) (string, error) {
	f.calls++
	return f.addr, f.err
}

type fakeFetch struct {
	calls   []string
	err     error
	sensors []Sensor
}

func (f *fakeFetch) Fetch(_ context.Context, addr string) ([]Sensor, error) {
	f.calls = append(f.calls, addr)
	return f.sensors, f.err
}

func newTestPoller(hint string, d *fakeDisc, f *fakeFetch) (*poller, *time.Time) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	p := newPoller("001788FFFE123456", hint, d, f, newMetrics("001788FFFE123456", time.Hour, prometheus.NewRegistry()))
	p.now = func() time.Time { return now }
	return p, &now
}

func TestDiscoveryBackoffAfterFailures(t *testing.T) {
	d := &fakeDisc{err: errors.New("no advertisement")}
	p, now := newTestPoller("", d, &fakeFetch{})
	ctx := context.Background()

	p.step(ctx) // t=0: first attempt runs immediately
	if d.calls != 1 {
		t.Fatalf("calls = %d, want 1", d.calls)
	}

	// Polls every minute for 5m-1s must not retry discovery.
	*now = now.Add(5*time.Minute - time.Second)
	p.step(ctx)
	if d.calls != 1 {
		t.Fatalf("retried inside the 5m backoff: calls = %d", d.calls)
	}

	*now = now.Add(time.Second) // t=5m: second attempt, then 10m wait
	p.step(ctx)
	if d.calls != 2 {
		t.Fatalf("calls = %d, want 2 at 5m", d.calls)
	}
	*now = now.Add(10*time.Minute - time.Second)
	p.step(ctx)
	if d.calls != 2 {
		t.Fatalf("retried inside the 10m backoff: calls = %d", d.calls)
	}

	*now = now.Add(time.Second) // t=15m: third attempt, then 20m wait
	p.step(ctx)
	if d.calls != 3 {
		t.Fatalf("calls = %d, want 3", d.calls)
	}
	*now = now.Add(20*time.Minute - time.Second)
	p.step(ctx)
	if d.calls != 3 {
		t.Fatalf("retried inside the 20m backoff: calls = %d", d.calls)
	}
	*now = now.Add(time.Second) // t=35m: fourth, and the wait stays 20m
	p.step(ctx)
	if d.calls != 4 {
		t.Fatalf("calls = %d, want 4", d.calls)
	}
	*now = now.Add(20 * time.Minute)
	p.step(ctx)
	if d.calls != 5 {
		t.Fatalf("wait should cap at 20m: calls = %d, want 5", d.calls)
	}
}

func TestConfirmedAddressSurvivesTransientFailures(t *testing.T) {
	d := &fakeDisc{addr: "192.0.2.77"}
	f := &fakeFetch{sensors: []Sensor{{ID: "x"}}}
	p, _ := newTestPoller("192.0.2.2", d, f)
	ctx := context.Background()

	p.step(ctx) // works
	f.err = &unreachableError{errors.New("timeout")}
	p.step(ctx)
	p.step(ctx)
	if p.addr != "192.0.2.2" || d.calls != 0 {
		t.Fatalf("two failures must keep the known-good address: addr=%q discover calls=%d", p.addr, d.calls)
	}
	f.err = nil
	p.step(ctx) // recovery resets the count
	f.err = &unreachableError{errors.New("timeout")}
	p.step(ctx)
	p.step(ctx)
	if p.addr == "" {
		t.Fatal("failure count should have reset after a success")
	}
	p.step(ctx) // third consecutive failure: forget the address
	if p.addr != "" {
		t.Fatalf("addr = %q, want cleared after %d consecutive failures", p.addr, confirmedFailuresBeforeRediscovery)
	}

	f.err = nil
	p.step(ctx) // rediscovers immediately (backoff is clean) and polls
	if d.calls != 1 || p.addr != "192.0.2.77" {
		t.Fatalf("after rediscovery: calls=%d addr=%q", d.calls, p.addr)
	}
}

// Regression: discovery that keeps returning an address that doesn't answer
// (stale cache, another device, CN mismatch) must back off like any other
// failed discovery, not re-run on every poll.
func TestDiscoveredButUnreachableAddressBacksOff(t *testing.T) {
	d := &fakeDisc{addr: "192.0.2.99"}
	f := &fakeFetch{err: &unreachableError{errors.New("certificate is for bridge other")}}
	p, now := newTestPoller("", d, f)
	ctx := context.Background()

	for i := 0; i < 5; i++ { // five 1-minute polls
		p.step(ctx)
		*now = now.Add(time.Minute)
	}
	if d.calls != 1 {
		t.Fatalf("discovery ran %d times in 5 minutes, want 1 (backoff engaged)", d.calls)
	}

	*now = now.Add(time.Second) // past 5m: second attempt, then 10m wait
	p.step(ctx)
	if d.calls != 2 {
		t.Fatalf("calls = %d, want 2 after the 5m wait", d.calls)
	}
	*now = now.Add(9 * time.Minute)
	p.step(ctx)
	if d.calls != 2 {
		t.Fatalf("calls = %d, still inside the 10m wait", d.calls)
	}
}

func TestConfirmingDiscoveredAddressResetsBackoff(t *testing.T) {
	d := &fakeDisc{err: errors.New("none")}
	f := &fakeFetch{sensors: []Sensor{{ID: "x"}}}
	p, now := newTestPoller("", d, f)
	ctx := context.Background()

	p.step(ctx) // fail -> 5m wait
	*now = now.Add(5 * time.Minute)
	d.err, d.addr = nil, "192.0.2.2"
	p.step(ctx) // discovered and the first poll works: backoff cleared
	if p.addr != "192.0.2.2" || p.unconfirmed {
		t.Fatalf("addr=%q unconfirmed=%v", p.addr, p.unconfirmed)
	}
	if got := testutil.ToFloat64(p.metrics.discovers.WithLabelValues("success")); got != 1 {
		t.Errorf("discoveries{success} = %v, want 1 (counted when the address is confirmed)", got)
	}
	if got := testutil.ToFloat64(p.metrics.discovers.WithLabelValues("failure")); got != 1 {
		t.Errorf("discoveries{failure} = %v, want 1", got)
	}

	// Bridge vanishes for good: three failures drop the address, then the
	// next discovery failure should wait 5m again, not 10m.
	f.err = &unreachableError{errors.New("gone")}
	for i := 0; i < confirmedFailuresBeforeRediscovery; i++ {
		p.step(ctx)
	}
	d.err = errors.New("none")
	p.step(ctx)
	*now = now.Add(5 * time.Minute)
	before := d.calls
	p.step(ctx)
	if d.calls != before+1 {
		t.Errorf("backoff was not reset by the confirmed success: calls %d -> %d", before, d.calls)
	}
}

func TestNonNetworkErrorKeepsAddress(t *testing.T) {
	d := &fakeDisc{addr: "192.0.2.2"}
	f := &fakeFetch{err: errors.New("GET temperature: HTTP 403")}
	p, _ := newTestPoller("192.0.2.2", d, f)

	p.step(context.Background())
	if p.addr != "192.0.2.2" || d.calls != 0 {
		t.Errorf("a 403 must not trigger discovery: addr=%q calls=%d", p.addr, d.calls)
	}
}

func TestMetricsExposeOnlyValidSensorsAndDropStaleOnes(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics("001788FFFE123456", 3*time.Minute, reg)
	m.now = func() time.Time { return time.Unix(2000, 0) }
	temp, bat := 24.51, 100.0
	m.pollSucceeded([]Sensor{
		{ID: "d1", Name: "Hallway sensor", Room: "Hallway", Temperature: &temp, Changed: time.Unix(1000, 0), Battery: &bat},
		{ID: "d2", Name: "No reading", Room: "Outside"},
	}, time.Unix(2000, 0))

	if n, err := testutil.GatherAndCount(reg, "hue_temperature_celsius"); err != nil || n != 1 {
		t.Fatalf("temperature series = %d (%v), want 1 (sensor without a reading must not emit)", n, err)
	}

	m.pollSucceeded(nil, time.Unix(2000, 0))
	if n, _ := testutil.GatherAndCount(reg, "hue_temperature_celsius"); n != 0 {
		t.Errorf("series for a vanished sensor lingered: %d", n)
	}
}

func TestMetricsWithholdSensorsWhenPollingGoesStale(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := newMetrics("001788FFFE123456", 3*time.Minute, reg)
	temp := 24.5
	m.pollSucceeded([]Sensor{{ID: "d1", Name: "Hallway sensor", Temperature: &temp}}, time.Unix(1000, 0))

	m.now = func() time.Time { return time.Unix(1000+180, 0) } // exactly 3m: still fresh
	if n, _ := testutil.GatherAndCount(reg, "hue_temperature_celsius"); n != 1 {
		t.Fatalf("fresh snapshot series = %d, want 1", n)
	}
	m.now = func() time.Time { return time.Unix(1000+181, 0) }
	if n, _ := testutil.GatherAndCount(reg, "hue_temperature_celsius"); n != 0 {
		t.Errorf("stale snapshot still exported (%d series); an hours-old reading would look current", n)
	}
	if n, _ := testutil.GatherAndCount(reg, "hue_exporter_last_poll_success_timestamp_seconds"); n != 1 {
		t.Error("last-success timestamp must keep being exported while stale")
	}
}
