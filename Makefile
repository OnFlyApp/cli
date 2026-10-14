VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: build test vet lint

build:
	CGO_ENABLED=0 go build -ldflags "-X github.com/axitdev/onfly/internal/mcp.Version=$(VERSION)" -o bin/onfly .

test:
	CGO_ENABLED=0 go test ./...

vet:
	go vet ./...

lint: vet
	go run honnef.co/go/tools/cmd/staticcheck@latest ./...
