package exporter

import (
	"context"
	"testing"
	"time"
)

func TestDefaultCAIsUsable(t *testing.T) {
	if _, err := newBridgeClient("key", "001788FFFE123456", DefaultCA); err != nil {
		t.Fatalf("embedded Signify CA doesn't parse: %v", err)
	}
}

func TestRunValidatesConfig(t *testing.T) {
	ctx := context.Background()
	for name, cfg := range map[string]Config{
		"no bridge id": {ApplicationKey: "k", PollInterval: time.Minute},
		"no key":       {BridgeID: "x", PollInterval: time.Minute},
		"fast poll":    {BridgeID: "x", ApplicationKey: "k", PollInterval: time.Second},
	} {
		if err := Run(ctx, cfg); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}
