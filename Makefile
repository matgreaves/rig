.PHONY: build test

# Build rigd with reproducible flags to ./bin/
build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/rigd ./cmd/rigd

# Build rigd, then run tests with RIG_BINARY pointing at it
test: build
	RIG_BINARY=$(CURDIR)/bin/rigd go test ./...
