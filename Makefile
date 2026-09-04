# Litebase build tasks.
#
# The default target produces a single binary with the dashboard embedded,
# which is the artefact you deploy.

VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)
BINARY  := litebase
EMBED   := backend/cmd/litebase/frontend

.PHONY: all build frontend backend test test-race lint fmt vet clean dev run docker install-deps check

all: build

## build: compile the dashboard and the server into one binary
build: frontend backend

## frontend: compile the React dashboard into the Go embed directory
frontend:
	@# Vite is configured not to empty its output directory, because that
	@# directory holds a committed placeholder go:embed needs. Clearing stale
	@# hashed assets here keeps them from accumulating in the binary.
	rm -rf $(EMBED)/assets $(EMBED)/index.html
	cd frontend && npm ci --no-audit --no-fund && npm run build

## backend: compile the Go server (expects the dashboard to be built)
backend:
	cd backend && CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o ../$(BINARY) ./cmd/litebase
	@echo "built ./$(BINARY) ($(VERSION))"

## backend-only: compile the server without the dashboard, for API work
backend-only:
	cd backend && go build -ldflags "$(LDFLAGS)" -o ../$(BINARY) ./cmd/litebase

## test: run the backend test suite
test:
	cd backend && go test ./...

## test-race: run the tests under the race detector
test-race:
	cd backend && go test -race ./...

## test-cover: run tests and report coverage per package
test-cover:
	cd backend && go test -cover ./...

## vet: run go vet
vet:
	cd backend && go vet ./...

## fmt: format the Go source
fmt:
	cd backend && gofmt -w .

## typecheck: type-check the frontend without emitting
typecheck:
	cd frontend && npx tsc --noEmit

## check: everything CI should run
check: vet test typecheck

## dev: run the API server for development (frontend runs separately)
dev:
	cd backend && LITEBASE_DEV_MODE=true LITEBASE_LOG_FORMAT=text \
		LITEBASE_ALLOWED_ORIGINS=http://localhost:5173 \
		go run ./cmd/litebase

## dev-frontend: run the Vite dev server, proxying the API to :8090
dev-frontend:
	cd frontend && npm run dev

## run: run the built binary
run: build
	./$(BINARY)

## docker: build the container image
docker:
	docker build --build-arg VERSION=$(VERSION) -t litebase:$(VERSION) -t litebase:latest .

## clean: remove build artefacts
clean:
	rm -f $(BINARY)
	@# The placeholder in $(EMBED) is deliberately kept: removing it would
	@# break the build until the frontend is compiled again.
	rm -rf $(EMBED)/assets $(EMBED)/index.html
	rm -rf frontend/node_modules frontend/tsconfig.tsbuildinfo

## release: cross-compile static binaries for common Linux targets
release: frontend
	@mkdir -p dist
	cd backend && for target in linux/amd64 linux/arm64; do \
		os=$${target%/*}; arch=$${target#*/}; \
		echo "building $$os/$$arch"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath \
			-ldflags "$(LDFLAGS)" -o ../dist/$(BINARY)-$$os-$$arch ./cmd/litebase; \
	done
	@echo "binaries written to dist/"

## help: list the available targets
help:
	@grep -E '^## ' $(MAKEFILE_LIST) | sed 's/## //' | awk -F': ' '{printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
