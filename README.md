# tunnel

A self-hosted [ngrok](https://ngrok.com) / Tailscale-Funnel alternative you fully own — **your own client and server**, no third party.

A public **server** (the edge) behaves like a lightweight reverse proxy. A **client** opens a single persistent outbound WebSocket to it (so it works behind NAT/firewalls). Inbound public requests are multiplexed back down that one connection over [yamux](https://github.com/hashicorp/yamux) — one logical stream per request — and the client forwards them to your local service.

```
Browser ──HTTPS──▶  tunnel server  ──yamux stream over WebSocket──▶  tunnel client ──▶ localhost:3000
        myapp.tunnel.example.com                                         (behind NAT)
```

## Features

- **One static Go binary** — runs as `tunnel server` or `tunnel http <port>`.
- **yamux over WebSocket** — passes cleanly through Traefik/nginx L7 routers *or* runs standalone. No per-request connection setup → cheap for thousands of long-lived connections.
- **Long-connection first-class**: WebSocket upgrades, SSE/streaming (immediate flush), and idle-but-open sockets all work. No hard write timeout to sever them.
- **Dual TLS mode**: standalone on-demand ACME (per-host Let's Encrypt certs, no wildcard needed) *or* plain HTTP behind a TLS-terminating proxy.
- **Per-client tokens**, unlimited clients, requested-or-random subdomains, reserved names pinned to a token.
- **Cloud-native config**: defaults → file (JSON/TOML/YAML) → env (`TUNNEL_*`, `*_FILE` secrets) → flags. Zero-config-friendly.
- **Observability**: structured logs, Prometheus `/metrics`, JSON `/_tunnel/status`.

## Install

```bash
# npx (no install) — one command, both roles
npx @ur-link/tunnel server --domain tunnel.example.com
npx @ur-link/tunnel http 3000 --server wss://connect.tunnel.example.com --token <tok>

# npm (global)
npm i -g @ur-link/tunnel        # provides the `tunnel` command

# Homebrew
brew install ur-link/tap/tunnel

# Go
go install github.com/ur-link/tunnel/cmd/tunnel@latest

# Docker (GitHub Container Registry or Docker Hub)
docker run --rm ghcr.io/ur-link/tunnel:latest version
docker run --rm urlink/tunnel:latest version
```

The npm package ships a tiny launcher that resolves a prebuilt binary for your platform (or downloads it from the GitHub release on first run), so `npx` always works on darwin/linux/windows × amd64/arm64.

## Quick start (local)

```bash
go build -o tunnel ./cmd/tunnel

# Terminal 1 — server (dev: TLS off, ephemeral token printed to logs)
./tunnel server --domain lvh.me --tls-mode off --http-addr :8080 --control-addr :7000

# Terminal 2 — client (forward local :3000)
./tunnel http 3000 --server ws://127.0.0.1:7000 --token <token-from-server-logs> --name myapp

# Terminal 3
curl -H 'Host: myapp.lvh.me' http://127.0.0.1:8080/
```

(`lvh.me` and `*.lvh.me` resolve to 127.0.0.1, handy for local testing.)

## Configuration

Three interchangeable layers, later wins:

```
built-in defaults  →  config file (json|yaml|toml)  →  env (TUNNEL_*)  →  CLI flags
```

- File auto-detected at `./config.*`, `~/.tunnel/config.*`, `/etc/tunnel/config.*`, or `--config`.
- Every key has a `TUNNEL_*` env var (`tls_mode` → `TUNNEL_TLS_MODE`).
- Secrets accept a `*_FILE` variant (`TUNNEL_TOKENS_FILE`, `TUNNEL_TOKEN_FILE`) for Docker/K8s secret mounts.
- `tunnel server --print-config` dumps the resolved config (secrets redacted).

See [`examples/server.config.yaml`](examples/server.config.yaml) and [`examples/client.config.yaml`](examples/client.config.yaml) for every key.

### Token format

`token` or `token:reserved1|reserved2`, comma-separated inline or one-per-line in a file. A reserved name may only be claimed by its owning token; unreserved names are first-come-first-served.

## Deployment

### Standalone (own TLS)

```bash
tunnel server --domain tunnel.example.com --tls-mode acme [email protected]
```
Needs `*.tunnel.example.com` (and `connect.tunnel.example.com`) pointed at the host, and ports 443/7000 reachable. Certs are issued per-host on first request (TLS-ALPN-01) — no wildcard cert required. Compose version: [`deploy/docker-compose.standalone.yml`](deploy/docker-compose.standalone.yml).

### Behind Traefik (proxy does TLS)

See [`deploy/docker-compose.traefik.yml`](deploy/docker-compose.traefik.yml). The server runs `--tls-mode off --trust-forwarded`; Traefik supplies the wildcard cert via DNS-01 and routes `*.tunnel.example.com` → edge `:80` and `connect.tunnel.example.com` → control `:7000`.

### Docker

```bash
docker build -t tunnel .
docker run -p 80:80 -p 7000:7000 -p 9090:9090 \
  -e TUNNEL_DOMAIN=tunnel.example.com -e TUNNEL_TLS_MODE=off -e TUNNEL_TRUST_FORWARDED=true \
  -e TUNNEL_TOKENS_FILE=/run/secrets/tokens -v $PWD/secrets:/run/secrets:ro \
  tunnel server
```

## Architecture

| Package | Responsibility |
|---|---|
| `cmd/tunnel` | CLI dispatch (`server` / `http` / `version`) |
| `internal/config` | layered cloud-native config (defaults/file/env/flags) |
| `internal/proto` | handshake framing (Register / Response) |
| `internal/mux` | WebSocket↔`net.Conn` adapter + yamux session tuning |
| `internal/server` | edge reverse proxy, control plane, registry, auth, TLS, metrics |
| `internal/client` | persistent connect + accept-stream loop + local relay + reconnect |

The edge reuses the standard `net/http/httputil.ReverseProxy` (with `FlushInterval:-1` and `Hijack` for SSE/WebSocket); the only twist is that the transport's `DialContext` opens a yamux stream to the owning client session instead of dialing a TCP port.

## Observability

- `GET /metrics` — Prometheus (`tunnel_active_clients`, `tunnel_active_streams`, `tunnel_requests_total`, `tunnel_bytes_{in,out}_total`).
- `GET /_tunnel/status` — JSON list of live tunnels (host, label, active streams, request count, uptime).
- `GET /healthz` on the control and metrics listeners.

## Development

```bash
go test ./...          # unit + end-to-end (HTTP, SSE, WebSocket, concurrency)
go test -race ./internal/e2e/
```
