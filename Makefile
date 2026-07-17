VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

.PHONY: all frontend build run test lint docker clean

all: build

frontend:
	cd frontend && npm install && npm run build

build: frontend
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o pingway ./cmd/pingway

# build the Go binary without rebuilding the frontend (uses existing dist/)
go-build:
	go build -o pingway ./cmd/pingway

run: go-build
	DATA_DIR=./data LOG_FORMAT=text ./pingway

test:
	go vet ./...
	go test -race ./...

lint:
	golangci-lint run

docker:
	docker build --build-arg VERSION=$(VERSION) -t pingway:$(VERSION) .

clean:
	rm -f pingway
