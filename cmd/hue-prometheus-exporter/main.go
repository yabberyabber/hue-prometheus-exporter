// Command hue-prometheus-exporter exposes Philips Hue motion-sensor
// temperatures (and battery levels) as Prometheus metrics.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/yabberyabber/hue-prometheus-exporter/internal/exporter"
)

func main() {
	var cfg exporter.Config
	var keyFile, caFile string
	flag.StringVar(&cfg.BridgeID, "bridge-id", os.Getenv("HUE_BRIDGE_ID"),
		"ID of your Hue bridge, e.g. 001788FFFE123456 (env HUE_BRIDGE_ID). Required.")
	flag.StringVar(&keyFile, "application-key-file", os.Getenv("HUE_APPLICATION_KEY_FILE"),
		"file containing the Hue application key. Alternatively set HUE_APPLICATION_KEY (not a flag, so the key stays out of the process list).")
	flag.StringVar(&cfg.BridgeAddressHint, "bridge-address", os.Getenv("HUE_BRIDGE_ADDRESS"),
		"optional IP of the bridge to try first; otherwise it is found with mDNS")
	flag.StringVar(&cfg.MDNSInterface, "mdns-interface", os.Getenv("HUE_MDNS_INTERFACE"),
		"network interface to send mDNS queries from, e.g. eth0 (default: let the OS choose)")
	flag.StringVar(&caFile, "ca-file", os.Getenv("HUE_CA_FILE"),
		"PEM bundle to verify the bridge's certificate against (default: Signify's published root CA, built in)")
	flag.StringVar(&cfg.ListenAddress, "listen-address", envDefault("LISTEN_ADDRESS", ":9185"),
		"address to serve /metrics on")
	flag.DurationVar(&cfg.PollInterval, "poll-interval", 60*time.Second,
		"how often to read the bridge (minimum 5s)")
	flag.Parse()

	if cfg.BridgeID == "" {
		fatalUsage("--bridge-id is required (find it in the Hue app under Settings > My Hue system > your bridge, or at https://<bridge-ip>/api/config)")
	}
	key := strings.TrimSpace(os.Getenv("HUE_APPLICATION_KEY"))
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			log.Fatalf("reading application key file: %v", err)
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		fatalUsage("a Hue application key is required: set HUE_APPLICATION_KEY or pass --application-key-file (see README)")
	}
	cfg.ApplicationKey = key
	if caFile != "" {
		b, err := os.ReadFile(caFile)
		if err != nil {
			log.Fatalf("reading CA file: %v", err)
		}
		cfg.CAPEM = b
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := exporter.Run(ctx, cfg); err != nil {
		log.Fatal(err)
	}
}

func envDefault(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalUsage(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	flag.Usage()
	os.Exit(2)
}
