package exporter

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Config is everything Run needs.
type Config struct {
	BridgeID       string // e.g. 001788FFFE123456; how the bridge is recognised
	ApplicationKey string // Hue application key (the "username" the bridge issued)
	// BridgeAddressHint is an optional first guess for the bridge's address.
	// Leave empty to discover it with mDNS.
	BridgeAddressHint string
	MDNSInterface     string // optional NIC to send mDNS queries from
	CAPEM             []byte // PEM bundle to verify the bridge against; DefaultCA if empty
	ListenAddress     string // e.g. ":9185"
	PollInterval      time.Duration
	// Verbose logs every finished poll and every /metrics request with its
	// status and duration.
	Verbose bool
}

// Run polls the bridge and serves /metrics until ctx is cancelled.
func Run(ctx context.Context, cfg Config) error {
	if cfg.BridgeID == "" || cfg.ApplicationKey == "" {
		return errors.New("bridge ID and application key are required")
	}
	if cfg.PollInterval < 5*time.Second {
		return fmt.Errorf("poll interval %s is too short (minimum 5s)", cfg.PollInterval)
	}
	ca := cfg.CAPEM
	if len(ca) == 0 {
		ca = DefaultCA
	}
	client, err := newBridgeClient(cfg.ApplicationKey, cfg.BridgeID, ca)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	m := newMetrics(cfg.BridgeID, 3*cfg.PollInterval, reg)
	if cfg.BridgeAddressHint != "" {
		m.setAddress(cfg.BridgeAddressHint)
	}
	p := newPoller(cfg.BridgeID, cfg.BridgeAddressHint, newMDNSDiscoverer(cfg.MDNSInterface), client, m)
	p.verbose = cfg.Verbose
	go p.run(ctx, cfg.PollInterval)

	mux := http.NewServeMux()
	var metricsHandler http.Handler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})
	if cfg.Verbose {
		metricsHandler = logRequests(metricsHandler)
	}
	mux.Handle("/metrics", metricsHandler)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprintln(w, "ok") })
	srv := &http.Server{Addr: cfg.ListenAddress, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdown)
	}()
	log.Printf("hue-prometheus-exporter for bridge %s listening on %s (poll every %s)", cfg.BridgeID, cfg.ListenAddress, cfg.PollInterval)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// statusRecorder remembers the status code a handler wrote.
type statusRecorder struct {
	http.ResponseWriter
	code int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.code = code
	s.ResponseWriter.WriteHeader(code)
}

// logRequests logs the status and duration of every request h handles.
func logRequests(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, code: http.StatusOK}
		h.ServeHTTP(rec, r)
		log.Printf("%s %s handled: status=%d duration=%s", r.Method, r.URL.Path, rec.code, time.Since(start).Round(time.Millisecond))
	})
}
