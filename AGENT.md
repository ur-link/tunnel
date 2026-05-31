# AGENT.md

Working rules, code style, and patterns for this repo. **Not** a product spec — see the doc map below for what the system does.

`tunnel` is a self-hosted ngrok alternative in Go: a public edge server + a client that opens one outbound WebSocket and has inbound requests multiplexed back over yamux. Module `github.com/ur-link/tunnel`; npm `@urlink/tunnel`; images `ghcr.io/ur-link/tunnel` + `urlink/tunnel`.

## Doc map (specs live here, not in this file)

- [README.md](README.md) — product overview, install, usage.
- [docs/architecture.md](docs/architecture.md) — packages, data path, control handshake, edge routing, **HTTP/API surfaces**.
- [docs/testing.md](docs/testing.md) — how tests are structured and run.
- [docs/TLS.md](docs/TLS.md) — TLS modes (`acme`/`dns`/`file`/`off`), certificates, persistence.
- [docs/multi-tenant.md](docs/multi-tenant.md) — namespaces, identity, hub/admin, discovery, roadmap.

When a feature is added or changed, update the relevant doc above (and this file if a *rule/pattern* changed) in the same change.

## Build & toolchain

- **Go 1.26+**, single module. On this machine the shell `GOROOT` is stale — run go as `GOROOT=/opt/homebrew/opt/go/libexec PATH=/opt/homebrew/opt/go/libexec/bin:$PATH go …`.
- `make build | test | test-race | lint | generate | docker | snapshot`.
- **templ**: UI components are `internal/web/*.templ`; generated `*_templ.go` is **committed** so `go build` works without the templ CLI. Run `make generate` (`go run github.com/a-h/templ/cmd/templ@latest generate`) after editing `.templ`.
- Zero runtime third-party network deps in the binary: **embed assets** (`embed.FS`), **never load from a CDN** (htmx is vendored at `internal/web/static/`).

## Code style

- Idiomatic Go; `gofmt` clean (generated `*_templ.go` excluded). `go vet` clean.
- Comments explain **why**, not what; match surrounding density. Doc-comment exported identifiers.
- **No silent failures** — handle/log errors; don't swallow them (best-effort cleanup may log + continue, and must say so).
- Keep files focused and small; one clear purpose per file/type.
- Tests: table-driven, fake OS commands (never shell out), `httptest` for handlers, in-process e2e; pass under `-race`. See [docs/testing.md](docs/testing.md).

## Core patterns (established this session)

- **Config** (`internal/config`): layered **defaults → file (json/yaml/toml) → env `TUNNEL_*` → flags**; every key has all three. Read back via koanf **typed getters** (`k.String/Int/Bool`), not struct `Unmarshal` (coerces env/flag strings). Durations are strings parsed with `mustDur`. Secrets accept a `*_FILE` env variant. Flags are kebab-case mapping to underscore keys via `FlagSet.Visit` (only set flags override). Add `--print-config` coverage for new server keys.
- **Transport**: one WebSocket per client, yamux-multiplexed (`internal/mux`). Server opens streams; client accepts. Tune yamux window + keepalive; never assume per-request connections.
- **Edge reverse proxy** (`server/session.go`): `httputil.ReverseProxy` with `FlushInterval:-1` and a `statusRecorder` (Hijack+Flush) for WS/SSE; transport `DialContext` opens a yamux stream. **No hard `WriteTimeout`** on public servers (kills long-lived streams) — use `ReadHeaderTimeout`+`IdleTimeout`+yamux keepalive.
- **Identity & naming** (`server/auth.go`, `identity_crud.go`): tokens carry `namespace` + `role`; inline grammar `token[@namespace][:reserved]`; writable JSON store (`users_file`) for admin CRUD merged with inline/bootstrap tokens. Services are single-level `<slug>-<namespace>.<domain>` by default (one `*.<domain>` wildcard) — nested `<slug>.<namespace>.<domain>` only with `tls-mode=dns`. Reserve names atomically via the registry (placeholder → attach).
- **Hot reload**: tokens file and `file`-mode certs reload in place; active tunnels live in the **registry** (independent of the identity store), so reload never drops connections. Listen addrs + base domain need a restart.
- **Persistence**: file-backed stores write atomically (tmp + rename); load at boot; tolerate a missing file. Empty/partial reads must not wipe live state.
- **TLS**: four modes (`acme` per-host autocert · `dns` lego wildcard · `file` mounted+hot-reload · `off` behind proxy). DNS providers are a **curated set** (avoid lego's all-providers aggregator — binary bloat). See [docs/TLS.md](docs/TLS.md).
- **Discovery** (`internal/discover`): lsof/netstat → runtime classify → project-root slug (prefer manifest name: package.json/go.mod/Cargo/pyproject). Within a project: lowest-port process = main (clean slug), other processes suffixed `-<port>`, same-process extra ports collapse. Path containment is symlink-resolved.
- **UI**: server-rendered templ + HTMX, OKLCH design tokens (modern-minimal), 8-state interactive controls, responsive; assets embedded. Browser auth = httpOnly token cookie; CLI = Bearer.

## Releases

- **Conventional commits** drive semantic-release: `feat`→minor, `fix`/`perf`/`refactor`→patch, `docs`/`chore`/`ci`/`build`/`test`→no release. Use accurate types — a `feat`/`fix` ships a version.
- Push to `master` runs the pipeline: goreleaser (6 targets) → per-platform npm packages + launcher → Homebrew cask → GitHub release; the release event triggers the multi-arch Docker build (GHCR + Docker Hub). Secrets: `NPM_TOKEN`, `GH_PAT`, `DOCKER_PAT`.
- Commit/push only when asked; branch off `master`/`main` for non-trivial work.
