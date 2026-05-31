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

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tunnel /usr/bin/tunnel
# Edge HTTP / control / metrics (HTTPS 443 only used in standalone tls-mode=acme).
EXPOSE 80 443 7000 9090
ENTRYPOINT ["/usr/bin/tunnel"]
CMD ["server"]
