.PHONY: build test clean clean-bin clean-logs clean-cache

# Build rigd with reproducible flags to ./bin/
build:
	CGO_ENABLED=0 go build -trimpath -buildvcs=false -o bin/rigd ./cmd/rigd

# Build rigd, then run tests with RIG_BINARY pointing at it
test: build
	RIG_BINARY=$(CURDIR)/bin/rigd go test ./...

# Remove build artifacts, logs, and cache
clean: clean-bin clean-logs clean-cache

# Remove build artifacts
clean-bin:
	rm -rf bin/

# Remove event log files (.rig/logs/)
clean-logs:
	rm -rf .rig/logs/

# Remove artifact cache (.rig/cache/)
clean-cache:
	rm -rf .rig/cache/
