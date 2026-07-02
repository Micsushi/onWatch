GO ?= go
BINARY ?= onwatch
GO_PACKAGES := ./...
GO_FILES := $(shell git ls-files '*.go')

.PHONY: build clean coverage ci dev fmt fmt-check integration lint release-local run style test test-race vet

fmt:
	gofmt -w $(GO_FILES)

fmt-check:
	@sh scripts/check-gofmt.sh

style: fmt-check

vet:
	$(GO) vet $(GO_PACKAGES)

lint: fmt-check vet

test:
	$(GO) test -count=1 $(GO_PACKAGES)

test-race:
	$(GO) test -race -coverprofile=coverage.out -covermode=atomic -count=1 $(GO_PACKAGES)

build:
	$(GO) build -o $(BINARY) .

ci: lint test-race build

run:
	$(GO) run . --debug --interval 10

clean:
	./app.sh --clean

coverage:
	$(GO) test -coverprofile=coverage.out -covermode=atomic -count=1 $(GO_PACKAGES)

integration:
	$(GO) test -v -tags=integration $(GO_PACKAGES)

dev:
	$(GO) run . --debug --interval 10

release-local:
	./app.sh --release
