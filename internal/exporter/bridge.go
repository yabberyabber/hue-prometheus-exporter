package exporter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Sensor is one Hue motion sensor's latest reading. Temperature and Battery
// are nil when the bridge has no value for them (e.g. a sensor whose
// temperature service is disabled or hasn't reported).
type Sensor struct {
	ID          string // owning device's resource id
	Name        string
	Room        string
	Temperature *float64
	Changed     time.Time // when the temperature last changed, per the bridge
	Battery     *float64
}

// unreachableError marks a failure to talk to the address at all (connect,
// timeout, or TLS identity mismatch) as opposed to the bridge answering
// with an error. Only this kind of failure means the address is probably
// stale and worth re-discovering.
type unreachableError struct{ err error }

func (e *unreachableError) Error() string { return e.err.Error() }
func (e *unreachableError) Unwrap() error { return e.err }

// bridgeClient talks to the CLIP v2 API.
type bridgeClient struct {
	http *http.Client
	key  string
}

// newBridgeClient builds a client that verifies the bridge's certificate
// against caPEM (Signify's root CA) and requires its CN to equal bridgeID.
//
// This is done by hand because the bridge's certificate carries the bridge
// ID in the CN with no SAN, and Go's standard hostname check ignores CN. We
// connect by IP, so there is no hostname to check anyway: the CN match is
// what proves the thing at this address is our bridge.
func newBridgeClient(key, bridgeID string, caPEM []byte) (*bridgeClient, error) {
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("no certificates found in Hue CA bundle")
	}
	tr := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // replaced by VerifyPeerCertificate below
			VerifyPeerCertificate: func(raw [][]byte, _ [][]*x509.Certificate) error {
				return verifyBridgeCert(raw, roots, bridgeID)
			},
		},
		TLSHandshakeTimeout: 10 * time.Second,
		DisableKeepAlives:   true,
	}
	return &bridgeClient{http: &http.Client{Transport: tr, Timeout: 20 * time.Second}, key: key}, nil
}

func verifyBridgeCert(raw [][]byte, roots *x509.CertPool, bridgeID string) error {
	if len(raw) == 0 {
		return errors.New("bridge presented no certificate")
	}
	certs := make([]*x509.Certificate, len(raw))
	for i, r := range raw {
		c, err := x509.ParseCertificate(r)
		if err != nil {
			return err
		}
		certs[i] = c
	}
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter}); err != nil {
		return fmt.Errorf("bridge certificate not signed by the Hue CA: %w", err)
	}
	if cn := certs[0].Subject.CommonName; !strings.EqualFold(cn, bridgeID) {
		return fmt.Errorf("certificate is for bridge %q, expected %q", cn, bridgeID)
	}
	return nil
}

type resp[T any] struct {
	Errors []struct {
		Description string `json:"description"`
	} `json:"errors"`
	Data []T `json:"data"`
}

func getResource[T any](ctx context.Context, c *bridgeClient, addr, resource string) ([]T, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+addr+"/clip/v2/resource/"+resource, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("hue-application-key", c.key)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, &unreachableError{err}
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, &unreachableError{err}
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", resource, res.StatusCode)
	}
	var out resp[T]
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("GET %s: bad JSON: %w", resource, err)
	}
	if len(out.Errors) > 0 {
		return nil, fmt.Errorf("GET %s: bridge error: %s", resource, out.Errors[0].Description)
	}
	return out.Data, nil
}

type rid struct {
	RID   string `json:"rid"`
	RType string `json:"rtype"`
}

type temperatureRes struct {
	Owner       rid  `json:"owner"`
	Enabled     bool `json:"enabled"`
	Temperature struct {
		Valid  bool `json:"temperature_valid"`
		Report *struct {
			Changed     time.Time `json:"changed"`
			Temperature float64   `json:"temperature"`
		} `json:"temperature_report"`
	} `json:"temperature"`
}

type deviceRes struct {
	ID       string `json:"id"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

type devicePowerRes struct {
	Owner      rid `json:"owner"`
	PowerState struct {
		BatteryLevel *float64 `json:"battery_level"`
	} `json:"power_state"`
}

type roomRes struct {
	Children []rid `json:"children"`
	Metadata struct {
		Name string `json:"name"`
	} `json:"metadata"`
}

// Fetch reads every enabled temperature service on the bridge at addr,
// joined with its device name, room and battery level.
func (c *bridgeClient) Fetch(ctx context.Context, addr string) ([]Sensor, error) {
	temps, err := getResource[temperatureRes](ctx, c, addr, "temperature")
	if err != nil {
		return nil, err
	}
	devices, err := getResource[deviceRes](ctx, c, addr, "device")
	if err != nil {
		return nil, err
	}
	power, err := getResource[devicePowerRes](ctx, c, addr, "device_power")
	if err != nil {
		return nil, err
	}
	rooms, err := getResource[roomRes](ctx, c, addr, "room")
	if err != nil {
		return nil, err
	}
	return buildSensors(temps, devices, power, rooms), nil
}

func buildSensors(temps []temperatureRes, devices []deviceRes, power []devicePowerRes, rooms []roomRes) []Sensor {
	names := map[string]string{}
	for _, d := range devices {
		names[d.ID] = d.Metadata.Name
	}
	battery := map[string]*float64{}
	for _, p := range power {
		battery[p.Owner.RID] = p.PowerState.BatteryLevel
	}
	roomOf := map[string]string{}
	for _, r := range rooms {
		for _, ch := range r.Children {
			if ch.RType == "device" {
				roomOf[ch.RID] = r.Metadata.Name
			}
		}
	}

	var out []Sensor
	for _, t := range temps {
		// A disabled temperature service (e.g. an unused outdoor sensor) has
		// no report at all; leave it out rather than export a fake value.
		if !t.Enabled {
			continue
		}
		s := Sensor{
			ID:      t.Owner.RID,
			Name:    names[t.Owner.RID],
			Room:    roomOf[t.Owner.RID],
			Battery: battery[t.Owner.RID],
		}
		if t.Temperature.Valid && t.Temperature.Report != nil {
			v := t.Temperature.Report.Temperature
			s.Temperature = &v
			s.Changed = t.Temperature.Report.Changed
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
