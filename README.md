# EdgeGate

High-performance reverse proxy.

## Tests & Benchmarks

```bash
go test ./...                              # all tests
go test -bench=. ./internal/ratelimit/...  # benchmarks
```

## Load Testing

Requires [vegeta](https://github.com/tsenart/vegeta). Results go to `/tmp/edgegate-bench/`.

```bash
./scripts/start-proxy.sh                   # start upstream + proxy
./scripts/attack-proxy.sh                  # attack proxy
./scripts/upstream-direct.sh               # attack upstream directly (baseline)
```

All scripts are configurable via env vars (e.g. `RATE=5000 DURATION=10s ./scripts/attack-proxy.sh`).
