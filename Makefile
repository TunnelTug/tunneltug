# SPDX-License-Identifier: MIT
# Copyright (c) 2026 TunnelTug Contributors

BINARY   := tunneltug
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(DATE)

.PHONY: build vm-guest test vet lint clean docker run-server run-client

build: vm-guest
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

# Linux guest agent for the TunnelTug VM runtime (PID 1 of the VM), embedded by vm_image.go.
vm-guest:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -ldflags "-s -w" -o vmguest/tug-init ./cmd/tug-init

test:
	go test ./... -count=1 -race -timeout 2m

vet:
	go vet ./...

lint:
	golangci-lint run ./...

clean:
	rm -rf bin/ dist/ coverage.out coverage.html

docker:
	docker build -t $(BINARY):$(VERSION) .

run-server:
	go run . -mode server -token "$${TUNNELTUG_TOKEN:-changeme-dev-token-16}" -dev -domain localhost

run-client:
	go run . -mode client -token "$${TUNNELTUG_TOKEN:-changeme-dev-token-16}" -server 127.0.0.1 -insecure

run-anycast:
	go run . -mode anycast -anycast-config config/anycast.local.yaml

run-hub:
	go run . -mode hub -hub-memory -token "$${TUNNELTUG_TOKEN:-}"

gen-token:
	go run . -gen-token