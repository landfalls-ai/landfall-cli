.PHONY: build test lint

build:
	go build -o landfall ./cmd/landfall

test:
	go test ./...

lint:
	golangci-lint run ./...
