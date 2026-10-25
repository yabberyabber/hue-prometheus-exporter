# hue-prometheus-exporter

A Prometheus exporter for Philips Hue **motion sensors**: it reads each
sensor's temperature and battery level from your Hue bridge and serves them at
`/metrics`, so you can graph your home's temperature over time.

- Talks to the bridge's local CLIP v2 API; nothing leaves your network.
- Finds the bridge itself with mDNS, so a changing DHCP address doesn't break it.
- Verifies the bridge's TLS certificate (against Signify's published CA) instead
  of skipping verification.
- Discovers sensors, names and rooms from the bridge. Pair a new sensor and it
  shows up on the next poll; there is nothing to configure per device.

## Quick start

You need your **bridge ID** and a Hue **application key**.

**Bridge ID**: shown in the Hue app (Settings → My Hue system → your bridge),
or `curl -sk https://<bridge-ip>/api/config | jq -r .bridgeid`.

**Application key**: press the link button on the bridge, then within 30
seconds:

```bash
curl -sk -X POST https://<bridge-ip>/api \
  -d '{"devicetype":"hue-prometheus-exporter#mylaptop","generateclientkey":true}'
# [{"success":{"username":"<your application key>", ...}}]
```

(`-k` is acceptable for this one-off LAN call. The exporter itself always
verifies the bridge's certificate.)

Then:

```bash
export HUE_APPLICATION_KEY=<your application key>
go run ./cmd/hue-prometheus-exporter --bridge-id 001788FFFE123456
```

```bash
curl -s localhost:9185/metrics | grep ^hue_
```

Prefer a file to an environment variable? `--application-key-file /path/to/key`.
There is deliberately no `--application-key` flag, so the key never appears in
the process list.

### Flags

| Flag (env) | Default | |
|---|---|---|
| `--bridge-id` (`HUE_BRIDGE_ID`) | *required* | ID of your bridge |
| `--application-key-file` (`HUE_APPLICATION_KEY_FILE`) | | File holding the key (or set `HUE_APPLICATION_KEY`) |
| `--listen-address` (`LISTEN_ADDRESS`) | `:9185` | Where to serve `/metrics` (and `/healthz`) |
| `--poll-interval` | `60s` | How often to read the bridge (minimum 5s) |
| `--bridge-address` (`HUE_BRIDGE_ADDRESS`) | | Optional IP to try first instead of mDNS |
| `--mdns-interface` (`HUE_MDNS_INTERFACE`) | OS choice | NIC to send mDNS queries from; set on hosts with many interfaces |
| `--ca-file` (`HUE_CA_FILE`) | built in | CA bundle for the bridge certificate |

## Metrics

| Metric | Labels | Meaning |
|---|---|---|
| `hue_temperature_celsius` | `sensor`, `room`, `sensor_id` | Last temperature reported by the sensor |
| `hue_temperature_last_changed_timestamp_seconds` | same | When it last reported a changed value |
| `hue_sensor_battery_percent` | same | Battery level |
| `hue_exporter_last_poll_success_timestamp_seconds` | | Last successful poll of the bridge (0 if never) |
| `hue_bridge_info` | `bridge_id`, `address` | Constant 1; where the exporter currently finds the bridge |
| `hue_poll_errors_total` | | Failed polls (including cycles with no bridge address) |
| `hue_bridge_discoveries_total` | `result` | mDNS discovery attempts, `success` or `failure` |

Things worth knowing:

- Sensors **report on change**, not on a schedule, so a stable temperature can
  look unchanged for a long time. `..._last_changed_timestamp_seconds` is
  therefore not a health signal; use `hue_exporter_last_poll_success_timestamp_seconds`.
- Sensors whose temperature service is disabled, or that haven't reported, emit
  no temperature series.
- If polling stops working, all sensor series are withheld once the last
  success is older than three poll intervals. Otherwise Prometheus would stamp
  an hours-old reading with the current scrape time and it would look fresh.
- Labels come from names in the Hue app. Rename a sensor or move it to another
  room and its series changes; `sensor_id` stays stable.

A Prometheus scrape config:

```yaml
scrape_configs:
  - job_name: hue
    scrape_interval: 60s
    static_configs:
      - targets: ["localhost:9185"]
```

## Docker

```bash
docker build -t hue-prometheus-exporter .
docker run --rm --network host \
  -e HUE_APPLICATION_KEY -e HUE_BRIDGE_ID=001788FFFE123456 \
  hue-prometheus-exporter
```

`--network host` is needed for mDNS discovery (multicast doesn't cross Docker's
bridge networks), and on Docker Desktop (macOS/Windows) host networking doesn't
reach your LAN at all. In either case, drop `--network host`, publish the port
(`-p 9185:9185`) and pass `--bridge-address <ip>` instead; discovery is then
skipped and a changed bridge IP needs a restart with the new address.

## Kubernetes (Helm)

Create a Secret holding the application key, then install the chart with your
bridge ID:

```bash
kubectl create namespace hue
kubectl create secret generic hue-credentials -n hue \
  --from-literal=application-key="$HUE_APPLICATION_KEY"

helm install hue charts/hue-prometheus-exporter -n hue \
  --set bridge.id=001788FFFE123456 \
  --set applicationKey.existingSecret=hue-credentials
```

The image defaults to `ghcr.io/yabberyabber/hue-prometheus-exporter:<appVersion>`,
published by this repo's release workflow when a `vX.Y.Z` tag is pushed (so a
version's image only exists once that tag has been pushed). Or build your own
from the `Dockerfile` and set `image.repository`/`image.tag`.

Settings you may want to change (see `charts/hue-prometheus-exporter/values.yaml`
for all of them):

| Value | Why |
|---|---|
| `mdnsInterface` | On nodes with several interfaces (docker/CNI bridges, VPNs), set this to the one on your LAN, e.g. `eth0`. |
| `hostNetwork` (default `true`) | mDNS needs the node's network namespace. The metrics port is then a node port and must be free. Set `false` only if you also set `bridge.addressHint`. |
| `metrics.bindAddress` | Empty binds all interfaces, which with `hostNetwork` exposes `/metrics` (including the bridge's address) on your LAN. Use `127.0.0.1` if Prometheus is on the same node with `hostNetwork` too. |
| `applicationKey.value` | Lets the chart create the Secret for you; the key then lands in your Helm values and release history, so prefer `existingSecret`. |

The chart exposes a ClusterIP Service named after the release for Prometheus to
scrape (disable with `service.enabled=false`).

## How discovery works

The bridge's IP is never configured. The exporter browses `_hue._tcp` over
mDNS and picks the advertisement whose `bridgeid` matches `--bridge-id`.

- **Identity is checked over TLS, not trusted from mDNS.** Whatever address is
  found (or given with `--bridge-address`) is only used if the certificate it
  presents chains to Signify's root CA **and** its CN equals the bridge ID. (Hue
  bridge certificates carry the ID as the CN with no SAN, so Go's standard
  hostname check can't be used.) A stale IP that now belongs to another device
  fails closed, and the application key is only sent after the handshake succeeds.
- **When it re-discovers:** only after the address stops answering: a connect
  failure, timeout, or certificate mismatch, not an HTTP error. A
  previously-working address survives two consecutive failures and is dropped
  on the third, so one timeout doesn't trigger discovery.
- **Backoff:** a failed discovery is not retried for 5 minutes, then 10, then 20,
  staying at 20 until one succeeds. A discovery counts as successful only once
  the discovered address answers a poll, so a stale mDNS answer can't reset the
  backoff and cause a multicast query on every poll.

### Not implemented (yet)

Two more strategies are designed but deliberately not built. Each would be
another `Discoverer` (see `internal/exporter/discovery.go`) tried after mDNS
fails, behind the same backoff:

1. **Signify cloud discovery**: `GET https://discovery.meethue.com` lists bridges
   seen from your public IP with their local addresses. Needs internet, is rate
   limited, and tells Signify your public IP.
2. **Subnet scan**: probe `https://<ip>/api/config` across the LAN /24 and match
   `bridgeid`. Needs no multicast or internet but is ~254 connection attempts,
   so a last resort.

## Development

```bash
go vet ./... && go test ./...
helm lint charts/hue-prometheus-exporter --set bridge.id=X --set applicationKey.existingSecret=s
```

Tests run against fixtures in `internal/exporter/testdata` (the shape of the
bridge's CLIP v2 `temperature`, `device`, `device_power` and `room` resources)
and a fake bridge with a CN-only certificate.

## License

MIT, see [LICENSE](LICENSE).
