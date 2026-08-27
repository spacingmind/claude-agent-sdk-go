.PHONY: build test lint fmt fmt-check vet check ci

build:
	go build -buildvcs=false ./...

test:
	go test -race ./...

lint:
	golangci-lint run

GOFMT := $(shell command -v gofumpt 2> /dev/null || echo gofmt)

fmt:
	$(GOFMT) -l -w .

fmt-check:
	@files=$$($(GOFMT) -l .); \
	if [ -n "$$files" ]; then \
		echo "unformatted files:"; echo "$$files"; exit 1; \
	fi

vet:
	go vet ./...

check: build vet fmt-check lint test

ci: check
