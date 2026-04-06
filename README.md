# EdgeGate

High-performance reverse proxy.

## Supported

- HTTP reverse proxy built on Go stdlib with targeted custom control points.
- WebSocket proxying over HTTP/1.1 upgrade (HTTP/2 WebSocket is not
  implemented).
- Hot config reload for routing and rate-limit updates without process restart.
- Per-client IP token-bucket rate limiting (IPv4/IPv6 + trusted proxies).
- TLS listener cert/key runtime reload (TLS mode toggle requires listener
  restart).
- Single-node v0.1 architecture (no distributed coordination/state sharing).

- Kubernetes deployment via custom Gateway API controller with Helm chart.

## Not Yet Supported

- Active/passive upstream health checks.
- Built-in traffic metrics/statistics endpoint.

## Implementation Notes

- Reverse proxy core intentionally stays close to standard library behavior,
  with selective customizations for future control.
- WebSockets are handled via HTTP/1.1 upgrade with bidirectional streaming;
  RFC 8441 (HTTP/2 WebSocket) is not implemented.
- A lightweight custom polling watcher detects config changes and triggers
  reload.
- Rate limiting is token-bucket per resolved client IP with configurable
  trusted proxy handling.
- Updating TLS cert/key is reload-friendly, but enabling/disabling TLS on a
  listener requires restarting that listener (not the whole process).

## Next

- Planned next feature: upstream load balancing.

## Quick Start

### Run locally

```bash
go build ./cmd/edgegate
./edgegate -conf ./configs/edgegate.yaml
```

### Run with Docker

```bash
docker build -t edgegate .
docker run --rm -v ./configs/edgegate.yaml:/etc/edgegate/edgegate.yaml -p 8080:8080 edgegate
```

### Deploy to Kubernetes

```bash
# Install Gateway API CRDs
kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.2.1/standard-install.yaml

# Install with Helm
helm install edgegate ./k8s/helm

# Create a Gateway and HTTPRoute
kubectl apply -f k8s/samples/gateway.yaml
kubectl apply -f k8s/samples/httproute.yaml
```

## Configuration

EdgeGate uses a YAML config file. Changes are detected automatically (5s poll)
and applied without restart.

```yaml
listeners:
  - listen: ":8080"
    tls:
      enabled: true
      default_cert_file: "./cert.pem"
      default_key_file: "./key.pem"
    routes:
      - match:
          host: "app.example.com"
          path_prefix: "/api"
        upstream: "http://localhost:3000"
    rate_limit:
      enabled: true
      requests: 1000
      window: 1s
      client_ttl: 5m
```

## Tests & Benchmarks

```bash
go test ./...                              # all tests
go test -race ./...                        # race detector
go vet ./...                               # static analysis
go test -bench=. ./internal/ratelimit/...  # benchmarks
```

### E2E

Requires [Docker](https://docs.docker.com/get-docker/), [kind](https://kind.sigs.k8s.io/), [kubectl](https://kubernetes.io/docs/tasks/tools/), and [Helm](https://helm.sh/docs/intro/install/).

```bash
make k8s-e2e            # spin up kind cluster, run tests, tear down
make k8s-e2e-debug      # same but keeps cluster alive on failure
make k8s-e2e-teardown   # delete the kind cluster
```

## Load Testing

Only requirement is Docker. Results and HTML plots go to `/tmp/edgegate-bench/`.

```bash
# Terminal 1 — start upstream + edgegate
./bench/start-proxy.sh                     # default config (no rate limit)
./bench/start-proxy.sh tls                 # TLS config
./bench/start-proxy.sh ratelimit           # rate-limited config

# Terminal 2 — run load test
./bench/attack-proxy.sh                    # HTTP attack
./bench/attack-proxy.sh --tls             # HTTPS attack

# Terminal 3 — swap config mid-test
./bench/swap-config.sh ratelimit           # enable rate limiting
./bench/swap-config.sh base                # back to no rate limiting

# Standalone baseline (no edgegate in the path)
./bench/upstream-direct.sh
```

Defaults (rate, duration, ports, etc.) are centralized in `bench/bench.env`.
Config variants live in `bench/configs/` as YAML templates.

## Project Structure

```
cmd/edgegate/             Entry point
internal/
  config/                 YAML parsing, file watcher, validation
  proxy/                  Server lifecycle, routing, reverse proxy, rate limiting
configs/                  Example configuration
k8s/
  controller/             Gateway API controller
  helm/                   Helm chart
  samples/                Gateway and HTTPRoute examples
e2e/                      E2E test suite (kind)
bench/                    Load testing scripts, config variants, shared env
docker/
  edgegate/               Dockerfile for edgegate
  vegeta/                 Dockerfile for vegeta load tester
```
