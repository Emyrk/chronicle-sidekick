.PHONY: build test lint icons

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

icons:
	go generate .
