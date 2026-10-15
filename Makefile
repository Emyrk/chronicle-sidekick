.PHONY: build test lint icons

build:
	go build ./...

test:
	go test ./...

lint:
	go vet ./...

icons:
	go run ./scripts/uploader-icon packaging/stone.svg icons packaging/AppIcon.icns
