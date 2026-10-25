package exporter

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeFixture[T any](t *testing.T, name string) []T {
	t.Helper()
	var r resp[T]
	if err := json.Unmarshal(fixture(t, name), &r); err != nil {
		t.Fatal(err)
	}
	return r.Data
}

func TestBuildSensorsFromRealPayloads(t *testing.T) {
	got := buildSensors(
		decodeFixture[temperatureRes](t, "temperature"),
		decodeFixture[deviceRes](t, "device"),
		decodeFixture[devicePowerRes](t, "device_power"),
		decodeFixture[roomRes](t, "room"),
	)

	// The fixture has 3 temperature services; one is disabled with no
	// temperature_report and must be dropped.
	if len(got) != 2 {
		t.Fatalf("got %d sensors, want 2: %+v", len(got), got)
	}
	byName := map[string]Sensor{}
	for _, s := range got {
		byName[s.Name] = s
	}
	hall, ok := byName["Hallway sensor"]
	if !ok {
		t.Fatalf("no Hallway sensor in %+v", byName)
	}
	if hall.Room != "Hallway" {
		t.Errorf("room = %q, want Hallway", hall.Room)
	}
	if hall.Temperature == nil || *hall.Temperature != 24.51 {
		t.Errorf("temperature = %v, want 24.51", hall.Temperature)
	}
	if hall.Battery == nil || *hall.Battery != 100 {
		t.Errorf("battery = %v, want 100", hall.Battery)
	}
	if hall.Changed.IsZero() {
		t.Error("changed timestamp not parsed")
	}
	if out := byName["Outdoor sensor 1"]; out.Room != "Outside" || out.Temperature == nil {
		t.Errorf("outdoor sensor 1 = %+v", out)
	}
}

func TestBuildSensorsInvalidTemperatureKeepsSensorWithoutValue(t *testing.T) {
	temps := []temperatureRes{{Owner: rid{RID: "d1", RType: "device"}, Enabled: true}}
	got := buildSensors(temps, nil, nil, nil)
	if len(got) != 1 || got[0].Temperature != nil {
		t.Fatalf("want one sensor with nil temperature, got %+v", got)
	}
}

// testPKI makes a CA and a CN-only bridge leaf (no SAN, like the real
// bridge's) valid for 127.0.0.1.
func testPKI(t *testing.T, cn string) (caPEM []byte, leaf tls.Certificate) {
	t.Helper()
	caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "root-bridge"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)

	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: cn},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func fakeBridge(t *testing.T, leaf tls.Certificate) (addr string) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/clip/v2/resource/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("hue-application-key") != "sekrit" {
			http.Error(w, "nope", http.StatusForbidden)
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/clip/v2/resource/")
		w.Write(fixture(t, name))
	})
	srv := httptest.NewUnstartedServer(mux)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.Listener.Addr().String()
}

func TestFetchOverCNOnlyCertificate(t *testing.T) {
	ca, leaf := testPKI(t, "001788FFFE123456")
	addr := fakeBridge(t, leaf)

	c, err := newBridgeClient("sekrit", "001788fffe123456", ca) // case-insensitive CN match
	if err != nil {
		t.Fatal(err)
	}
	sensors, err := c.Fetch(context.Background(), addr)
	if err != nil {
		t.Fatal(err)
	}
	if len(sensors) != 2 {
		t.Errorf("got %d sensors, want 2", len(sensors))
	}
}

func TestFetchRejectsWrongBridge(t *testing.T) {
	ca, leaf := testPKI(t, "AAAAAAFFFEBBBBBB")
	addr := fakeBridge(t, leaf)

	c, _ := newBridgeClient("sekrit", "001788FFFE123456", ca)
	_, err := c.Fetch(context.Background(), addr)
	var un *unreachableError
	if !errors.As(err, &un) {
		t.Fatalf("a different bridge's cert must read as unreachable (triggers rediscovery), got %v", err)
	}
}

func TestFetchRejectsUntrustedCA(t *testing.T) {
	_, leaf := testPKI(t, "001788FFFE123456")
	otherCA, _ := testPKI(t, "whatever")
	addr := fakeBridge(t, leaf)

	c, _ := newBridgeClient("sekrit", "001788FFFE123456", otherCA)
	if _, err := c.Fetch(context.Background(), addr); err == nil {
		t.Fatal("cert from an untrusted CA should be rejected")
	}
}

func TestFetchBadKeyIsNotUnreachable(t *testing.T) {
	ca, leaf := testPKI(t, "001788FFFE123456")
	addr := fakeBridge(t, leaf)

	c, _ := newBridgeClient("wrong", "001788FFFE123456", ca)
	_, err := c.Fetch(context.Background(), addr)
	var un *unreachableError
	if err == nil || errors.As(err, &un) {
		t.Fatalf("a 403 must be a plain error (rediscovering would not help), got %v", err)
	}
}

func TestFetchConnectionRefusedIsUnreachable(t *testing.T) {
	ca, _ := testPKI(t, "001788FFFE123456")
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()

	c, _ := newBridgeClient("sekrit", "001788FFFE123456", ca)
	_, err := c.Fetch(context.Background(), addr)
	var un *unreachableError
	if !errors.As(err, &un) {
		t.Fatalf("want unreachable, got %v", err)
	}
}
