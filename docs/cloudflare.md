# Running behind Cloudflare (wildcard subdomains)

This guide wires `tunnel` to a Cloudflare zone so every tunnel is reachable at
`<slug>.t.ur.link` (or `<slug>-<namespace>.t.ur.link` with namespaces). The base
tunnel domain in the examples is **`t.ur.link`**; swap it for yours.

> Examples are **split by Traefik**. Prefer the **no‑Traefik** ones — the tunnel
> server is already a reverse proxy, so Traefik is only worth it if you run other
> sites on the same host. Composes live in [`../deploy/cloudflare/`](../deploy/cloudflare/).

## DNS: alias all subdomains with one wildcard

In the Cloudflare dashboard for `ur.link`, add:

| Type    | Name           | Content              | Proxy        |
| ------- | -------------- | -------------------- | ------------ |
| `A`/`AAAA` or `CNAME` | `*.t`          | your server IP / host | 🟠 or ⚪ (see below) |
| `A`/`AAAA` or `CNAME` | `connect.t`    | same                  | 🟠 or ⚪ |

`*.t` is the wildcard that aliases **every** `<anything>.t.ur.link` to your server —
flat service names like `web-meabed.t.ur.link` are a single label under `*.t`, so one
record covers them all. `connect.t.ur.link` is the client control plane (WebSocket);
it's served on the **edge** (`:443`), so clients use `wss://connect.t.ur.link` over the
same TLS — **no separate port to expose** (the legacy `:7000` listener still exists for
L4 setups but you don't need it here). It's even covered by the same `*.t.ur.link`
wildcard cert.

- **CNAME** (e.g. to a dynamic hostname) works the same as A/AAAA; Cloudflare flattens it.
- **🟠 Proxied (orange cloud)** — Cloudflare terminates TLS at its edge and forwards to your origin.
- **⚪ DNS‑only (grey cloud)** — Cloudflare just resolves DNS; TLS is terminated on **your** server.

### ⚠️ The wildcard‑SSL gotcha (read this first)

Cloudflare's **free Universal SSL covers only one level**: `ur.link` and `*.ur.link`.
A **two‑level** wildcard like `*.t.ur.link` proxied (🟠) is **not** covered for free —
you'd need **Advanced Certificate Manager** (ACM, paid) or Cloudflare for SaaS.

Three ways out, cheapest first:

1. **Use a one‑level base domain** — set `TUNNEL_DOMAIN=ur.link` so services are
   `web-meabed.ur.link`. Free Universal SSL + proxied works out of the box. ✅ simplest.
2. **Go DNS‑only (⚪) and let your server do TLS** — Let's Encrypt issues `*.t.ur.link`
   via **DNS‑01** (our `tls-mode=dns`, Cloudflare provider). Free, supports the deep wildcard. ✅ recommended for `*.t.ur.link`.
3. **Buy ACM** (~$10/mo) and keep `*.t.ur.link` proxied. 💰

WebSockets traverse Cloudflare's proxy fine, so `connect.t.ur.link` may be proxied or not.

## Scenario matrix

| # | Cloudflare DNS | Who issues the public TLS cert | Server `tls-mode` | Traefik | Compose |
| - | -------------- | ------------------------------ | ----------------- | ------- | ------- |
| 1 | 🟠 proxied | **Cloudflare** (edge) | `off` (or `file` w/ Origin Cert) | no  | [`proxied.yml`](../deploy/cloudflare/docker-compose.proxied.yml) |
| 2 | ⚪ DNS‑only | **your server** (Let's Encrypt DNS‑01) | `dns` (cloudflare) | no  | [`dns-acme.yml`](../deploy/cloudflare/docker-compose.dns-acme.yml) |
| 3 | ⚪ DNS‑only | **Traefik** (Let's Encrypt DNS‑01) | `off` + trust‑forwarded | yes | [`dns-traefik.yml`](../deploy/cloudflare/docker-compose.dns-traefik.yml) |
| 4 | 🟠 proxied (+ACM) | **Cloudflare** (edge) | `off`/`file` | yes | [`proxied-traefik.yml`](../deploy/cloudflare/docker-compose.proxied-traefik.yml) |

In every case the **tunnel server owns the tunnels** — Cloudflare/Traefik only handle
the public TLS + DNS; tunnel multiplexing (yamux over WebSocket) is always ours.

---

## 1 · Proxied, Cloudflare does all TLS — no Traefik  ✅ simplest

DNS: `*.t` and `connect.t` **🟠 proxied**. (Use base domain `ur.link` for free SSL, or ACM for `*.t.ur.link`.)

Cloudflare → **SSL/TLS → Overview**, pick a mode:
- **Flexible** — Cloudflare↔origin is **plain HTTP**; server runs `tls-mode=off` on `:80`. Easiest, but the CF↔origin hop is unencrypted over the internet — only acceptable if you firewall the origin to [Cloudflare's IP ranges](https://www.cloudflare.com/ips/) or use `cloudflared`.
- **Full (strict)** — recommended. Create a free **Cloudflare Origin Certificate** (SSL/TLS → Origin Server → Create Certificate, 15‑yr, covers `*.t.ur.link`), mount it, and run `tls-mode=file`. No ACME on the server, encrypted end‑to‑end.

```bash
DOMAIN=t.ur.link docker compose -f deploy/cloudflare/docker-compose.proxied.yml up -d
```

## 2 · DNS‑only, the server does all ACME — no Traefik  ✅ recommended for `*.t.ur.link`

DNS: `*.t` and `connect.t` **⚪ DNS‑only (grey)**, pointed at your server IP. Open ports 80 + 443 (control rides `:443` via `connect.t.ur.link`).

The server gets a real `*.t.ur.link` wildcard from Let's Encrypt via **DNS‑01** using a
scoped Cloudflare API token — no edge cert, no ACM, no Traefik.

1. Cloudflare → **My Profile → API Tokens → Create Token → Edit zone DNS** (Zone:DNS:Edit on `ur.link`). Copy the token.
2. `export CF_DNS_API_TOKEN=...` (or put it in a `.env` next to the compose).

```bash
DOMAIN=t.ur.link CF_DNS_API_TOKEN=*** \
  docker compose -f deploy/cloudflare/docker-compose.dns-acme.yml up -d
```

This is the cleanest free path for a deep wildcard: one `*.t.ur.link` cert, auto‑renewed in place.

## 3 · DNS‑only, Traefik does ACME

DNS: **⚪ grey**. Traefik holds the `*.t.ur.link` wildcard (DNS‑01 via the same Cloudflare token); the tunnel server runs `tls-mode=off --trust-forwarded` behind it.

```bash
DOMAIN=t.ur.link CF_DNS_API_TOKEN=*** \
  docker compose -f deploy/cloudflare/docker-compose.dns-traefik.yml up -d
```

## 4 · Proxied (+ACM), Traefik in front

DNS: **🟠 proxied** (needs ACM for `*.t.ur.link`). Cloudflare terminates public TLS; Traefik routes on the origin (Cloudflare Origin Cert, or HTTP behind CF Flexible); the tunnel server handles the tunnels.

```bash
DOMAIN=t.ur.link docker compose -f deploy/cloudflare/docker-compose.proxied-traefik.yml up -d
```

---

## After it's up

```bash
docker logs tunnel | grep ephemeral     # grab the auto-generated admin token
# expose a local app from anywhere:
npx @urlink/tunnel http 3000 --server wss://connect.t.ur.link --token <token> --name web
#   ➜  https://web.t.ur.link   (or web-<namespace>.t.ur.link with a namespaced token)
```

Path routing instead of subdomains? Set `TUNNEL_ROUTING_MODE=path` and reach services at
`https://<namespace>.t.ur.link/<slug>/` — see [multi-tenant.md](multi-tenant.md).
