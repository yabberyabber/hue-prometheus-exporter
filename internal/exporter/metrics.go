package exporter

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// metrics exposes the latest poll as gauges. Sensor gauges are rebuilt from
// the most recent snapshot on every scrape (a custom collector, not stored
// GaugeVecs) so a sensor that disappears from the bridge, or stops being
// valid, drops out of the series instead of freezing at its last value.
// If polling itself stops working, the whole snapshot is withheld once it is
// older than staleAfter: Prometheus stamps scraped samples with the scrape
// time, so an hours-old reading would otherwise look current.
type metrics struct {
	staleAfter time.Duration
	now        func() time.Time

	mu          sync.Mutex
	sensors     []Sensor
	lastSuccess time.Time
	address     string
	bridgeID    string

	pollErrors prometheus.Counter
	discovers  *prometheus.CounterVec
}

var (
	sensorLabels = []string{"sensor", "room", "sensor_id"}

	descTemp = prometheus.NewDesc("hue_temperature_celsius",
		"Last temperature reported by a Hue motion sensor, in degrees C.", sensorLabels, nil)
	descChanged = prometheus.NewDesc("hue_temperature_last_changed_timestamp_seconds",
		"Unix time the sensor last reported a changed temperature. Sensors only report on change, so a stale value is normal; compare against battery/reachability before alerting.", sensorLabels, nil)
	descBattery = prometheus.NewDesc("hue_sensor_battery_percent",
		"Hue motion sensor battery level.", sensorLabels, nil)
	descLastOK = prometheus.NewDesc("hue_exporter_last_poll_success_timestamp_seconds",
		"Unix time of the last successful poll of the bridge (0 if never).", nil, nil)
	descInfo = prometheus.NewDesc("hue_bridge_info",
		"Constant 1; the address label is where the bridge is currently believed to be.", []string{"bridge_id", "address"}, nil)
)

func newMetrics(bridgeID string, staleAfter time.Duration, reg prometheus.Registerer) *metrics {
	m := &metrics{
		bridgeID:   bridgeID,
		staleAfter: staleAfter,
		now:        time.Now,
		pollErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "hue_poll_errors_total", Help: "Failed polls of the bridge."}),
		discovers: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hue_bridge_discoveries_total", Help: "Bridge discovery attempts by result."}, []string{"result"}),
	}
	m.discovers.WithLabelValues("success")
	m.discovers.WithLabelValues("failure")
	reg.MustRegister(m, m.pollErrors, m.discovers)
	return m
}

func (m *metrics) pollSucceeded(s []Sensor, now time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sensors = s
	m.lastSuccess = now
}

func (m *metrics) pollFailed()             { m.pollErrors.Inc() }
func (m *metrics) discovery(result string) { m.discovers.WithLabelValues(result).Inc() }

func (m *metrics) setAddress(a string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.address = a
}

// Describe sends nothing, making this an "unchecked" collector: the set of
// label values is dynamic.
func (m *metrics) Describe(chan<- *prometheus.Desc) {}

func (m *metrics) Collect(ch chan<- prometheus.Metric) {
	m.mu.Lock()
	sensors, last, addr := m.sensors, m.lastSuccess, m.address
	m.mu.Unlock()

	var lastTS float64
	if !last.IsZero() {
		lastTS = float64(last.Unix())
	}
	ch <- prometheus.MustNewConstMetric(descLastOK, prometheus.GaugeValue, lastTS)
	if addr != "" {
		ch <- prometheus.MustNewConstMetric(descInfo, prometheus.GaugeValue, 1, m.bridgeID, addr)
	}
	if last.IsZero() || m.now().Sub(last) > m.staleAfter {
		return
	}
	for _, s := range sensors {
		lv := []string{s.Name, s.Room, s.ID}
		if s.Temperature != nil {
			ch <- prometheus.MustNewConstMetric(descTemp, prometheus.GaugeValue, *s.Temperature, lv...)
			ch <- prometheus.MustNewConstMetric(descChanged, prometheus.GaugeValue, float64(s.Changed.Unix()), lv...)
		}
		if s.Battery != nil {
			ch <- prometheus.MustNewConstMetric(descBattery, prometheus.GaugeValue, *s.Battery, lv...)
		}
	}
}
