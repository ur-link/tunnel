# Build a static binary, ship it on distroless. Same image runs server & client.
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/tunnel ./cmd/tunnel

# Root (not :nonroot) so the standalone edge can bind :80/:443 and write the ACME
# cache — the norm for proxy images (Caddy/Traefik). Run nonroot with mapped
# non-privileged ports + a writable cache volume if you prefer.
FROM gcr.io/distroless/static-debian12
COPY --from=build /out/tunnel /usr/bin/tunnel
# Conventional mount points (see docs/TLS.md):
#   /etc/tunnel  -> config.yaml|json|toml, tokens, file-mode certs   (read-only)
#   /data        -> ACME certificate cache                            (writable)
ENV TUNNEL_TLS_CACHE_DIR=/data/certs
# Edge HTTP / HTTPS / control / metrics.
EXPOSE 80 443 7000 9090
# Distroless-friendly healthcheck (no shell/curl needed): probe the control plane.
HEALTHCHECK --interval=30s --timeout=4s --start-period=10s --retries=3 \
  CMD ["/usr/bin/tunnel", "healthcheck"]
ENTRYPOINT ["/usr/bin/tunnel"]
CMD ["server"]
