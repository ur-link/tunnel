# TLS, certificates & persistence

`tunnel server` has three TLS modes. Pick the one that matches how you run it.

| Mode | Who issues/serves the cert | Challenges | Wildcard? | Use when |
|------|----------------------------|------------|-----------|----------|
| `acme` | the tunnel server (Go `autocert`) | **TLS‑ALPN‑01** (:443), **HTTP‑01** (:80) | ❌ per‑host only | Standalone, public, ports 80/443 reachable |
| `dns`  | the tunnel server (lego) | **DNS‑01** via your DNS provider | ✅ `*.<domain>` | You control DNS via a supported provider; want one wildcard + nested subdomains |
| `file` | **you** (mount a cert/key) | whatever you used to obtain it (incl. **DNS‑01**) | ✅ | You want a wildcard, an internal CA, or your own ACME tooling |
| `off`  | an upstream proxy (Traefik/nginx/Caddy) | the proxy's (often **DNS‑01** wildcard) | ✅ (proxy) | Behind a reverse proxy |

### `dns` mode (built-in DNS-01 wildcard)
```bash
tunnel server --domain tunnel.example.com --tls-mode dns \
  --tls-dns-provider cloudflare --acme-email you@example.com
# provider creds via env, e.g. CF_DNS_API_TOKEN=... (see go-acme/lego provider docs)
```
The server obtains a single `*.tunnel.example.com` cert on demand and renews it in
place — no per-host issuance, so no Let's Encrypt rate-limit risk. Built-in providers:
**cloudflare, digitalocean** — kept deliberately small (the heavy AWS/GCP SDKs are
excluded to keep the binary ~13 MB). Each reads its standard env vars
(`CF_DNS_API_TOKEN`, `DO_AUTH_TOKEN`). Need another provider? Import its lego
provider and add a case (accepting the size cost), or use `tls-mode=file` with a
cert from your own DNS-01 tooling.

**Nested subdomains** (`--nested-subdomains`, requires `dns` mode): services are addressed
`<slug>.<namespace>.<domain>` (e.g. `web.meabed.tunnel.example.com`) instead of the default
single-level `<slug>-<namespace>`. The server issues a `*.<namespace>.<domain>` wildcard per
namespace on demand. (Adds the `go-acme/lego` dependency; the binary is ~2× larger.)

> **Why no built‑in DNS‑01 / wildcard in `acme` mode?** The in‑process ACME client (`golang.org/x/crypto/acme/autocert`) only implements TLS‑ALPN‑01 and HTTP‑01. Those can't issue wildcard certs. For a wildcard you either bring your own cert (`file`) or let a proxy do DNS‑01 (`off`). This keeps the binary dependency‑light while still supporting every challenge type via the right mode.

## How each mode is wired

- **`acme`** — HTTPS edge on `:443` gets certs on demand; the first request for `app.your.domain` triggers issuance (gated to hosts that currently have a live tunnel, so nobody can spam issuance). `:80` serves the ACME HTTP‑01 challenge and 308‑redirects everything else to HTTPS. **Persist the cache dir** (`TUNNEL_TLS_CACHE_DIR`, default `~/.tunnel/certs`, `/data/certs` in Docker) or you'll re‑issue on every restart and hit Let's Encrypt rate limits.
- **`file`** — HTTPS edge serves `TUNNEL_TLS_CERT_FILE` + `TUNNEL_TLS_KEY_FILE`; `:80` redirects to HTTPS. The files are **hot‑reloaded** when they change on disk, so renewals (e.g. `acme.sh --cron`) are picked up without a restart.
- **`off`** — edge runs plain HTTP on `:80`; set `TUNNEL_TRUST_FORWARDED=true` so it honours the proxy's `X‑Forwarded‑*`. The proxy terminates TLS and routes `*.your.domain` → the container.

## Persistence & mounting (config + certs + tokens)

Everything is a file or a directory you can mount. Conventional layout used by the compose examples:

```
/etc/tunnel/            (read‑only mount — your config)
  config.yaml|json|toml   auto‑discovered (also searched in ./ and ~/.tunnel)
  tokens                  TUNNEL_TOKENS_FILE=/etc/tunnel/tokens
  certs/fullchain.pem     TUNNEL_TLS_CERT_FILE  (file mode)
  certs/key.pem           TUNNEL_TLS_KEY_FILE   (file mode)
/data/                  (writable volume — runtime state)
  certs/                  TUNNEL_TLS_CACHE_DIR  (acme cache; persist this)
```

Config resolves as **defaults → file → env (`TUNNEL_*`) → flags**, so you can mount a `config.yaml` *and* override single values with env — see [`../examples/server.config.yaml`](../examples/server.config.yaml). Secrets also accept a `*_FILE` env (`TUNNEL_TOKENS_FILE`) for Docker/K8s secret mounts.

---

## The easiest setup for each case

### Case 1 — Standalone, automatic per‑host certs (`acme`)

Simplest public setup. DNS: point `*.tunnel.example.com` **and** `connect.tunnel.example.com` at the host; open 80/443/7000.

```bash
mkdir -p config && printf 'sometoken:myapp\n' > config/tokens
docker run -d --name tunnel --restart unless-stopped \
  -p 80:80 -p 443:443 -p 7000:7000 \
  -e TUNNEL_DOMAIN=tunnel.example.com \
  -e TUNNEL_TLS_MODE=acme -e [email protected] \
  -e TUNNEL_TOKENS_FILE=/etc/tunnel/tokens \
  -v "$PWD/config:/etc/tunnel:ro" \
  -v tunnel-data:/data \
  ghcr.io/ur-link/tunnel:latest
```
Compose: [`docker-compose.standalone.yml`](../deploy/docker-compose.standalone.yml). The `tunnel-data` volume keeps issued certs across restarts.

### Case 2 — Standalone, **wildcard** cert via DNS‑01 (`file`)

Issue the wildcard once with any DNS‑01 tool, mount it, done. The server hot‑reloads on renewal.

```bash
# one‑time issue (Cloudflare shown; any acme.sh/lego/certbot DNS plugin works)
acme.sh --issue --dns dns_cf -d tunnel.example.com -d '*.tunnel.example.com'
acme.sh --install-cert -d tunnel.example.com \
  --fullchain-file ./tunnel/certs/fullchain.pem \
  --key-file       ./tunnel/certs/key.pem
printf 'sometoken:myapp\n' > ./tunnel/tokens

docker run -d --name tunnel --restart unless-stopped \
  -p 80:80 -p 443:443 -p 7000:7000 \
  -e TUNNEL_DOMAIN=tunnel.example.com -e TUNNEL_TLS_MODE=file \
  -e TUNNEL_TLS_CERT_FILE=/etc/tunnel/certs/fullchain.pem \
  -e TUNNEL_TLS_KEY_FILE=/etc/tunnel/certs/key.pem \
  -e TUNNEL_TOKENS_FILE=/etc/tunnel/tokens \
  -v "$PWD/tunnel:/etc/tunnel:ro" \
  ghcr.io/ur-link/tunnel:latest
```
Compose: [`docker-compose.file-tls.yml`](../deploy/docker-compose.file-tls.yml).

### Case 3 — Behind Traefik / nginx / Caddy (`off`)

The proxy owns TLS (typically a DNS‑01 wildcard) and routes to the tunnel over plain HTTP.

```bash
docker run -d --name tunnel --restart unless-stopped \
  -e TUNNEL_DOMAIN=tunnel.example.com \
  -e TUNNEL_TLS_MODE=off -e TUNNEL_TRUST_FORWARDED=true \
  -e TUNNEL_TOKENS_FILE=/etc/tunnel/tokens \
  -v "$PWD/config:/etc/tunnel:ro" \
  ghcr.io/ur-link/tunnel:latest
```
Compose examples:
- shared external Traefik network: [`docker-compose.traefik-external.yml`](../deploy/docker-compose.traefik-external.yml)
- Traefik + the tunnel together: [`docker-compose.traefik.yml`](../deploy/docker-compose.traefik.yml)

---

## Notes

- **Control plane TLS:** the client control endpoint (`:7000`) is plain HTTP/ws on its own port so it can be routed independently. In `off` mode the proxy gives it `wss` (route `connect.your.domain` → `:7000`). In standalone `acme`/`file` mode, front `:7000` with TLS (or keep it on a trusted network) — the auth token is bearer‑sent, so use `wss` in production.
- **Running non‑root:** the image runs as root so it can bind 80/443 and write the cache (like Caddy/Traefik). To run non‑root, listen on high ports (`TUNNEL_HTTP_ADDR=:8080`, `TUNNEL_HTTPS_ADDR=:8443`), publish `-p 80:8080 -p 443:8443`, and give the cache volume to your uid.
