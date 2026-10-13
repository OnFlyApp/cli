.PHONY: build test

build:
	CGO_ENABLED=0 go build -o bin/onfly .

test:
	CGO_ENABLED=0 go test ./...
