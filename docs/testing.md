# Testing

## Run

```bash
make test          # go test ./...
make test-race     # go test -race -count=1 ./...   (use this before pushing)
make lint          # gofmt -l . check + go vet ./...
```

CI (`.github/workflows/ci.yml`) runs the race suite + `gofmt` + `go vet` on linux/macOS/windows, plus a cross-compile of every release target.

## Conventions

- **Unit tests** are table-driven and live beside the code (`*_test.go`, package-internal).
- **OS commands are faked, never executed.** Discovery tests inject a `fakeRunner`
  returning canned `lsof`/`ps` output (`internal/discover/discover_test.go`); never shell
  out to real `lsof`/`netstat` in tests.
- **HTTP handlers** use `net/http/httptest` (e.g. admin auth gate in `identity_test.go`).
- **End-to-end** (`internal/e2e`) boots the real server + client in-process on random ports
  with a local echo/SSE/WebSocket app and asserts full round-trips (plain HTTP, SSE streaming,
  WebSocket upgrade, concurrency, unknown-host handling). No external services.
- **Persistence / reload** tests round-trip through temp files (`t.TempDir()`).
- Everything must pass under `-race`.
- `gofmt` must be clean; **generated `*_templ.go` files are excluded** from the fmt check.

## What can't be unit-tested here

Live ACME issuance (`tls-mode=acme`/`dns`) needs real Let's Encrypt + DNS-provider
credentials, so only the config/routing/naming/wildcard-mapping plumbing is unit-tested.
Verify TLS end-to-end manually with a self-signed cert (`tls-mode=file`) or a staging ACME run.

## Manual / live verification

Each feature in this project was also verified live (real `tunnel server` + `tunnel http`/`auto`
binaries, real curl/WebSocket round-trips) before commit — prefer a quick live check over
trusting unit tests alone for transport, TLS, and discovery changes. Use `lvh.me` /
`*.lvh.me` (resolve to 127.0.0.1) for local subdomain testing.
