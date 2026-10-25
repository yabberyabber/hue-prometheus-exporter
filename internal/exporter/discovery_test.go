package exporter

import (
	"net"
	"strings"
	"testing"

	"github.com/hashicorp/mdns"
)

func TestPickBridgeMatchesByBridgeIDCaseInsensitive(t *testing.T) {
	entries := []*mdns.ServiceEntry{
		{Name: "other", AddrV4: net.ParseIP("192.0.2.9"), InfoFields: []string{"bridgeid=aaaaaafffebbbbbb", "modelid=BSB002"}},
		{Name: "Hue Bridge - 123456", AddrV4: net.ParseIP("192.0.2.2"), InfoFields: []string{"bridgeid=001788fffe123456", "modelid=BSB003"}},
	}
	got, err := pickBridge(entries, "001788FFFE123456")
	if err != nil {
		t.Fatal(err)
	}
	if got != "192.0.2.2" {
		t.Errorf("got %s, want 192.0.2.2", got)
	}
}

func TestPickBridgeNoMatchListsWhatItSaw(t *testing.T) {
	entries := []*mdns.ServiceEntry{
		{AddrV4: net.ParseIP("192.0.2.9"), InfoFields: []string{"bridgeid=aaaaaafffebbbbbb"}},
		{AddrV4: net.ParseIP("192.0.2.8")}, // no TXT bridgeid: ignored
	}
	_, err := pickBridge(entries, "001788FFFE123456")
	if err == nil || !strings.Contains(err.Error(), "aaaaaafffebbbbbb") {
		t.Fatalf("want error naming the bridge it did see, got %v", err)
	}
}

func TestPickBridgeEmpty(t *testing.T) {
	if _, err := pickBridge(nil, "X"); err == nil {
		t.Fatal("want error for no entries")
	}
}
