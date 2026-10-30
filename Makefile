# Go runs in the official image: nothing to install but Docker.
GO_IMAGE ?= golang:1.27.3-alpine
GO = docker run --rm -v "$(CURDIR)":/src -w /src \
	-e GOCACHE=/src/.cache/go-build -e GOMODCACHE=/src/.cache/mod -e CGO_ENABLED=0 \
	$(GO_IMAGE)
LOCAL = docker compose -f examples/local/compose.yaml

.PHONY: test vet fmt check build local-up local-down local-logs

check: fmt vet test

test:
	$(GO) go test ./...

vet:
	$(GO) go vet ./...

fmt:
	@out="$$($(GO) gofmt -l .)"; if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

build:
	$(GO) go build -trimpath -o bin/standby ./cmd/standby

local-up:
	$(LOCAL) up -d --build

local-down:
	$(LOCAL) down

local-logs:
	$(LOCAL) logs -f guardian
