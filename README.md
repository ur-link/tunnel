# tunnel

Self-hosted ngrok / Tailscale-Funnel alternative — your own client **and** server.

- **Go** single static binary (`tunnel server` / `tunnel http <port>`).
- Persistent outbound client→server connection (works behind NAT).
- **yamux multiplexing over WebSocket/TLS** — passes through Traefik/nginx L7 routers or runs standalone.
- **Dual edge/TLS mode**: standalone on-demand ACME, or plain-HTTP behind a TLS-terminating proxy.
- **Per-client tokens**, requested-or-random subdomains, unlimited clients.
- HTTP/HTTPS + WebSocket tunneling with first-class long-lived-connection (WS/SSE) support.
- Logs + Prometheus `/metrics` + JSON status API.

See the design plan for full architecture and build order.
