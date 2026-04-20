BINARY   := anansi
MODULE   := github.com/anansi-bench/anansi
VERSION  := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-X main.Version=$(VERSION)"

.PHONY: build test lint clean install validate smoke

## Build the binary
build:
	go build $(LDFLAGS) -o bin/$(BINARY) ./cmd/anansi

## Install to $GOPATH/bin
install:
	go install $(LDFLAGS) ./cmd/anansi

## Run all tests
test:
	go test -v -race ./...

## Run tests with coverage
test-cover:
	go test -v -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

## Lint (requires golangci-lint)
lint:
	golangci-lint run ./...

## Validate the full matrix config
validate:
	go run ./cmd/anansi validate --config configs/matrix-full.yaml

## Quick smoke test (dry run)
smoke:
	go run ./cmd/anansi run --config configs/matrix-smoke.yaml --dry-run

## Deploy benchmark infrastructure
deploy:
	kubectl apply -f deploy/rbac.yaml
	kubectl apply -f deploy/daemonset-cachedrop.yaml

## Tear down benchmark infrastructure
undeploy:
	kubectl delete -f deploy/daemonset-cachedrop.yaml --ignore-not-found
	kubectl delete -f deploy/rbac.yaml --ignore-not-found

## Clean build artefacts
clean:
	rm -rf bin/ coverage.out coverage.html results/

## Show help
help:
	@echo "Anansi — Cold-Start Bottleneck Hunter"
	@echo ""
	@echo "Targets:"
	@grep -E '^## ' Makefile | sed 's/## /  /'
