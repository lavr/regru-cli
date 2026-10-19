MODULE  := github.com/lavr/regru-cli
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT  ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE    ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS := -X $(MODULE)/internal/cli.Version=$(VERSION) -X $(MODULE)/internal/cli.Commit=$(COMMIT) -X $(MODULE)/internal/cli.Date=$(DATE)

.PHONY: build cross test lint

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o regru ./cmd/regru

cross:
	@mkdir -p dist
	@for os in linux darwin; do \
		for arch in amd64 arm64; do \
			echo "build dist/regru-$$os-$$arch"; \
			CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o dist/regru-$$os-$$arch ./cmd/regru || exit 1; \
		done; \
	done

test:
	go vet ./...
	go test -race ./...

lint:
	golangci-lint run ./...
