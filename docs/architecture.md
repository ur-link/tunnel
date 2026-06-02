# Architecture & API flow

Product overview: [README](../README.md). TLS specifics: [TLS.md](TLS.md). Multi-tenant model: [multi-tenant.md](multi-tenant.md).

## Packages

| Package | Responsibility |
|---|---|
| `cmd/tunnel` | CLI dispatch: `server` · `http <target>` · `auto [path]` · `version` |
| `internal/config` | layered config (defaults → file → env `TUNNEL_*` → flags), one struct per binary |
| `internal/proto` | control handshake framing (`Register` → `Response`) + `ControlPath` |
| `internal/mux` | WebSocket ↔ `net.Conn` adapter + yamux session tuning |
| `internal/server` | edge reverse proxy, control plane, registry, identity/auth, TLS, metrics, web UI wiring |
| `internal/client` | persistent connect + accept-stream loop + local relay + reconnect; `auto.go` orchestrates discovery |
| `internal/discover` | local dev-server discovery (lsof/netstat → runtime classify → project slug) |
| `internal/web` | templ + HTMX admin console & status pages (embedded assets) |
| `internal/logging` | slog logger (auto text/json) |

## System overview

```mermaid
flowchart LR
  B["Browser / API client"]
  subgraph SRV["tunnel server"]
    E["Edge :80/:443<br/>routing + reverse proxy"]
    C["Control :7000<br/>WebSocket + yamux"]
    M["Metrics :9090<br/>/metrics and /_tunnel/status"]
    RG[("registry<br/>host to session")]
    ID[("identity store<br/>tokens and namespaces")]
    SS[("service store<br/>persistent")]
  end
  subgraph CLI["tunnel client behind NAT"]
    CL["client"]
    A1["localhost:3000"]
    A2["localhost:8080"]
  end
  B -->|"HTTPS host-routed"| E
  E --> RG
  E -. "admin and hub UI" .-> ID
  E -. status .-> SS
  CL -->|"outbound wss + Bearer"| C
  C --> ID
  C --> RG
  C --> SS
  E ==>|"yamux stream per request"| CL
  CL --> A1
  CL --> A2
```

## Data path (one public request)

```mermaid
sequenceDiagram
  participant B as Browser
  participant E as Edge
  participant Rg as Registry
  participant S as yamux session
  participant Cl as Client
  participant App as Local app
  B->>E: HTTPS request, Host web-meabed.ur.link
  E->>Rg: lookup host
  Rg-->>E: session
  E->>S: ReverseProxy DialContext opens stream
  S-->>Cl: accept stream
  Cl->>App: dial 127.0.0.1:3000 and relay
  App-->>Cl: response HTTP / WS upgrade / SSE
  Cl-->>S: bytes
  S-->>E: bytes
  E-->>B: response, immediate flush, Hijack for WS/SSE
```

One WebSocket per client; **yamux** multiplexes one stream per inbound request. The server opens streams, the client accepts them — no per-request connection setup.

## Control handshake (`internal/proto`, `server/control.go`, `client/client.go`)

1. Client dials `wss://<control>/_tunnel/connect` with `Authorization: Bearer <token>` (or `?token=`).
2. Server authenticates, wraps the WS as a `net.Conn`, reads `proto.Register{Name, HostHeader, ClientVersion}`.
3. Server reserves a hostname (namespace-aware), replies `proto.Response{OK, Hostname, URL}` (or `OK:false, Error`).
4. Both wrap the conn in yamux (server = `yamux.Server`, client = `yamux.Client`); server registers `host → session`.
5. On disconnect: client reconnects with backoff; server unregisters and marks the service offline.

```mermaid
sequenceDiagram
  participant Cl as Client
  participant C as Control endpoint
  participant Rg as Registry
  Cl->>C: wss /_tunnel/connect (Authorization: Bearer)
  C->>C: authenticate token
  Cl->>C: Register{name, host_header, version}
  C->>Rg: reserve hostname (namespace-aware)
  alt name taken / bad token
    C-->>Cl: Response{ok:false, error}
  else ok
    C-->>Cl: Response{ok:true, hostname, url}
    Note over Cl,C: both wrap the conn in yamux
    C->>Rg: attach host to session
    loop per inbound request, until disconnect
      C->>Cl: Open() stream
    end
    Cl--xC: disconnect
    C->>Rg: release host (mark offline)
    Cl->>C: reconnect (backoff + jitter)
  end
```

## Edge host routing (`server/edge.go`)

`edgeRoute(host)` strips port + base domain → `sub`, `full`:
- `sub == control-host` (default `connect`) → the **control plane** (`/_tunnel/connect`), so clients reach it over the edge's TLS at `wss://connect.<domain>` — a single port, no separate `:7000` exposure (the dedicated control listener still runs for behind-proxy/L4 setups).
- `sub == "admin"` → admin console/API.
- bare namespace label (no dot, known namespace) → that namespace's **hub** — `handleHub` in subdomain mode, or `handlePathNamespace` in path mode (`routing_mode=path`: services at `/<slug>/`, prefix stripped, `tn_route` affinity cookie).
- otherwise → **service**: `registry.lookup(full)` → session proxy. Service hosts are `<slug>-<ns>.<domain>` (flat) or `<slug>.<ns>.<domain>` (nested).

```mermaid
flowchart TD
  H["request Host"] --> U{"under base domain?"}
  U -->|no| NF["404"]
  U -->|yes| SUB["sub = host minus domain"]
  SUB --> AD{"sub is admin?"}
  AD -->|yes| ADM["admin console / API"]
  AD -->|no| NSQ{"bare label and<br/>known namespace?"}
  NSQ -->|yes| HUB["namespace hub<br/>status page / API"]
  NSQ -->|no| REG{"registry has host?"}
  REG -->|yes| PX["proxy to client session"]
  REG -->|no| BG["502 not connected"]
```

## HTTP surfaces

| Listener | Path | Auth | Purpose |
|---|---|---|---|
| control (`:7000`) | `/_tunnel/connect` | Bearer token | client WebSocket + yamux |
| control / metrics | `/healthz` | none | liveness |
| metrics (`:9090`) | `/metrics` | none (bind localhost) | Prometheus |
| metrics | `/_tunnel/status?namespace=` | none | JSON service list |
| edge `admin.<domain>` | `/api/users` (GET/POST), `/api/users/{token}` (PATCH/DELETE), `/api/users/{token}/rotate` (POST), `/api/services` | admin Bearer | identity CRUD JSON API |
| edge `admin.<domain>` | `/`, `/login`, `/users*`, `/partials/services`, `/_static/*` | admin cookie | web console |
| edge `<namespace>.<domain>` | `/api/services` | namespace token / admin | hub JSON API |
| edge `<namespace>.<domain>` | `/`, `/login`, `/partials/services` | namespace cookie | status page |

Browser auth = the token in an httpOnly cookie (`tn_admin` / `tn_hub`); CLI/automation = `Authorization: Bearer`.

## Reverse-proxy core

`httputil.ReverseProxy` with `FlushInterval: -1` (SSE/streaming) and a `statusRecorder` exposing `Hijack`/`Flush` (WebSocket upgrades). The transport's `DialContext` opens a yamux stream to the session instead of dialing TCP — the only change from a normal reverse proxy. No hard `WriteTimeout` (would sever long-lived streams); rely on `ReadHeaderTimeout` + `IdleTimeout` + yamux keepalive.

## Discovery (`tunnel auto`)

`internal/discover` finds local dev servers and the client orchestrator (`client/auto.go`) opens one tunnel per service, rescanning on an interval.

```mermaid
flowchart TD
  SC["lsof / netstat<br/>listening TCP ports"] --> CR["classify runtime<br/>node, python, ..."]
  CR --> PR["walk to project root<br/>.git, package.json, go.mod"]
  PR --> SL["slug = manifest name<br/>package.json / go.mod / Cargo, else folder"]
  SL --> GP["group by project:<br/>lowest-port proc is main (clean slug),<br/>other procs get -port, same-proc HMR collapses"]
  GP --> PF{"project root<br/>under --path?"}
  PF -->|no| SK["skip, contained"]
  PF -->|yes| EX["expose slug-ns.domain<br/>one client per service"]
```
