BINARY   := tunneltug
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT   ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo none)
DATE     ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || echo unknown)
LDFLAGS  := -s -w -X main.Version=$(VERSION) -X main.Commit=$(COMMIT) -X main.BuildDate=$(DATE)

.PHONY: build test vet lint clean docker run-server run-client

build:
	go build -ldflags "$(LDFLAGS)" -o bin/$(BINARY) .

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