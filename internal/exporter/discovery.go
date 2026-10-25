package exporter

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"time"

	"github.com/hashicorp/mdns"
)

// Discoverer finds the bridge's current LAN address. Callers must still
// verify the bridge identity over TLS; a Discoverer is only a hint.
//
// Only mDNS is implemented. Two further strategies are designed but
// deliberately not built yet (see the README's "How discovery works"): Signify's cloud endpoint
// (https://discovery.meethue.com) and a subnet scan matching /api/config's
// bridgeid. Either would be another Discoverer, tried in order after mDNS
// fails, behind the same backoff.
type Discoverer interface {
	Discover(ctx context.Context, bridgeID string) (addr string, err error)
}

const hueService = "_hue._tcp"

// mdnsDiscoverer browses _hue._tcp and picks the entry whose bridgeid TXT
// record matches. Needs to share the host's network namespace (pod
// hostNetwork) for multicast to work.
type mdnsDiscoverer struct {
	iface   string // optional; empty means let the library choose
	timeout time.Duration
	// browse is swapped out in tests.
	browse func(ctx context.Context, iface *net.Interface, timeout time.Duration) ([]*mdns.ServiceEntry, error)
}

func newMDNSDiscoverer(iface string) *mdnsDiscoverer {
	return &mdnsDiscoverer{iface: iface, timeout: 5 * time.Second, browse: browseHue}
}

func (d *mdnsDiscoverer) Discover(ctx context.Context, bridgeID string) (string, error) {
	var iface *net.Interface
	if d.iface != "" {
		var err error
		if iface, err = net.InterfaceByName(d.iface); err != nil {
			return "", fmt.Errorf("mdns interface %q: %w", d.iface, err)
		}
	}
	entries, err := d.browse(ctx, iface, d.timeout)
	if err != nil {
		return "", fmt.Errorf("mdns browse: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return pickBridge(entries, bridgeID)
}

func browseHue(ctx context.Context, iface *net.Interface, timeout time.Duration) ([]*mdns.ServiceEntry, error) {
	ch := make(chan *mdns.ServiceEntry, 32)
	var entries []*mdns.ServiceEntry
	done := make(chan struct{})
	go func() {
		defer close(done)
		for e := range ch {
			entries = append(entries, e)
		}
	}()
	err := mdns.QueryContext(ctx, &mdns.QueryParam{
		Service:     hueService,
		Domain:      "local",
		Timeout:     timeout,
		Interface:   iface,
		Entries:     ch,
		DisableIPv6: true,
		Logger:      log.New(io.Discard, "", 0),
	})
	close(ch)
	<-done
	return entries, err
}

// pickBridge returns "ip" for the entry advertising bridgeid=<bridgeID>.
// Matching on the TXT record, not the hostname, because the advertised
// hostname is the MAC without the FFFE the bridge ID has in the middle.
func pickBridge(entries []*mdns.ServiceEntry, bridgeID string) (string, error) {
	var seen []string
	for _, e := range entries {
		id := txtValue(e.InfoFields, "bridgeid")
		if id == "" {
			continue
		}
		seen = append(seen, id)
		if strings.EqualFold(id, bridgeID) {
			// IPv4 only: an IPv6 literal would need brackets in the URL, and
			// the browse has IPv6 disabled anyway.
			ip := e.AddrV4
			if ip == nil {
				return "", fmt.Errorf("bridge %s advertised without an address", bridgeID)
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no %s advertisement for bridge %s (saw %d: %v)", hueService, bridgeID, len(seen), seen)
}

func txtValue(fields []string, key string) string {
	prefix := key + "="
	for _, f := range fields {
		if strings.HasPrefix(f, prefix) {
			return strings.TrimPrefix(f, prefix)
		}
	}
	return ""
}
