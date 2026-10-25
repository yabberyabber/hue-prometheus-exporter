package exporter

import (
	"context"
	"errors"
	"log"
	"strconv"
	"time"
)

// confirmedFailuresBeforeRediscovery is how many consecutive unreachable
// polls a previously-working address survives before it is thrown away. One
// 20s timeout or a bridge reboot shouldn't force a multicast discovery that
// might itself fail and leave us addressless for up to 20 minutes.
const confirmedFailuresBeforeRediscovery = 3

type sensorFetcher interface {
	Fetch(ctx context.Context, addr string) ([]Sensor, error)
}

// poller keeps one bridge address, polls it, and re-discovers the address
// only when it stops answering. Discovery is gated by a backoff so a bridge
// that is down or unplugged costs one multicast query per 5/10/20 minutes
// instead of one per poll.
type poller struct {
	bridgeID string
	addr     string // current address; empty means "needs discovery"
	// unconfirmed is true for an address that discovery just returned and
	// that has not yet answered a poll. Discovery only counts as having
	// succeeded once the address works: a stale or wrong mDNS answer must
	// keep the backoff growing, not reset it.
	unconfirmed bool
	// failures counts consecutive unreachable polls of a confirmed address.
	failures int

	disc    Discoverer
	fetch   sensorFetcher
	backoff backoff
	metrics *metrics
	verbose bool // log every finished poll
	now     func() time.Time
}

// newPoller starts from hintAddr (may be empty). A hint is treated as
// confirmed: it is the operator's claim, and is not gated by the backoff.
func newPoller(bridgeID, hintAddr string, d Discoverer, f sensorFetcher, m *metrics) *poller {
	return &poller{bridgeID: bridgeID, addr: hintAddr, disc: d, fetch: f, metrics: m, now: time.Now}
}

// step runs one poll cycle.
func (p *poller) step(ctx context.Context) {
	if p.addr == "" && !p.discover(ctx) {
		if ctx.Err() == nil {
			p.metrics.pollFailed() // no address this cycle counts as a failed poll
		}
		return
	}

	start := time.Now()
	sensors, err := p.fetch.Fetch(ctx, p.addr)
	if ctx.Err() != nil {
		return
	}
	if p.verbose {
		log.Printf("poll finished: status=%s duration=%s", pollStatus(err), time.Since(start).Round(time.Millisecond))
	}
	if err != nil {
		p.metrics.pollFailed()
		var un *unreachableError
		if !errors.As(err, &un) {
			log.Printf("poll failed: %v", err)
			return
		}
		p.unreachable(err)
		return
	}

	if p.unconfirmed {
		p.backoff.Success()
		p.metrics.discovery("success")
		p.unconfirmed = false
	}
	p.failures = 0
	p.metrics.pollSucceeded(sensors, p.now())
}

// pollStatus is the HTTP status of a finished poll: 200 on success, the
// bridge's status if it refused a request, "none" if no status is known
// (transport failure or an unparseable body).
func pollStatus(err error) string {
	if err == nil {
		return "200"
	}
	var se *statusError
	if errors.As(err, &se) {
		return strconv.Itoa(se.code)
	}
	return "none"
}

// unreachable handles a transport-level failure of the current address.
func (p *poller) unreachable(err error) {
	if p.unconfirmed {
		// Discovery handed us an address that doesn't work: that is a failed
		// discovery, and must back off like one.
		p.backoff.Failure(p.now())
		p.metrics.discovery("failure")
		log.Printf("discovered address %s is not the bridge (next discovery no sooner than %s): %v",
			p.addr, p.backoff.notUntil.Format(time.RFC3339), err)
		p.forget()
		return
	}
	p.failures++
	log.Printf("bridge unreachable at %s (%d/%d): %v", p.addr, p.failures, confirmedFailuresBeforeRediscovery, err)
	if p.failures >= confirmedFailuresBeforeRediscovery {
		p.forget()
	}
}

func (p *poller) forget() {
	p.addr, p.unconfirmed, p.failures = "", false, 0
	p.metrics.setAddress("")
}

// discover returns true if p.addr is now set (not yet confirmed working).
func (p *poller) discover(ctx context.Context) bool {
	if !p.backoff.Allowed(p.now()) {
		return false
	}
	addr, err := p.disc.Discover(ctx, p.bridgeID)
	if ctx.Err() != nil {
		return false // shutting down; not a discovery failure
	}
	if err != nil {
		p.backoff.Failure(p.now())
		p.metrics.discovery("failure")
		log.Printf("bridge discovery failed (next attempt no sooner than %s): %v",
			p.backoff.notUntil.Format(time.RFC3339), err)
		return false
	}
	log.Printf("discovered bridge %s at %s", p.bridgeID, addr)
	p.addr, p.unconfirmed = addr, true
	p.metrics.setAddress(addr)
	return true
}

func (p *poller) run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		p.step(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
