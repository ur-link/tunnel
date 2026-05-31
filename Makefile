.PHONY: build install test test-race lint fmt vet run-server run-client docker snapshot clean

BIN     ?= tunnel
VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)

build: ## Build the local binary
	go build -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/tunnel

install: ## go install the binary
	go install -ldflags "$(LDFLAGS)" ./cmd/tunnel

test: ## Run tests
	go test ./...

test-race: ## Run tests with the race detector
	go test -race -count=1 ./...

lint: fmt-check vet ## gofmt check + go vet

fmt: ## Format code
	gofmt -w .

fmt-check: ## Fail if code is not gofmt-clean
	@test -z "$$(gofmt -l .)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

vet: ## go vet
	go vet ./...

run-server: build ## Run a dev server (TLS off)
	./$(BIN) server --domain lvh.me --tls-mode off --http-addr :8080 --control-addr :7000

run-client: build ## Run a dev client to :3000 (set TOKEN=...)
	./$(BIN) http 3000 --server ws://127.0.0.1:7000 --token "$(TOKEN)" --name myapp

docker: ## Build the Docker image
	docker build --build-arg VERSION=$(VERSION) -t $(BIN):$(VERSION) .

snapshot: ## Cross-platform snapshot build via goreleaser (no publish)
	goreleaser release --clean --snapshot --skip=publish,announce

clean:
	rm -rf $(BIN) dist npm/dist
