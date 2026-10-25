FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /hue-prometheus-exporter ./cmd/hue-prometheus-exporter

# Static binary, no shell or CA store needed: the only TLS peer is the Hue
# bridge, verified against the CA bundle compiled into the binary.
FROM scratch
COPY --from=builder /hue-prometheus-exporter /hue-prometheus-exporter
USER 65534:65534
EXPOSE 9185
ENTRYPOINT ["/hue-prometheus-exporter"]
