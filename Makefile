.PHONY: build build-all test fmt clean clean-bin clean-logs clean-cache

PLATFORMS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64

# Build rigd for the host platform to ./bin/
# Kills any running rigd so stale processes don't serve old code.
build:
	cd internal && CGO_ENABLED=0 go build -trimpath -buildvcs=false -o ../bin/rigd ./cmd/rigd
	@pkill -x rigd 2>/dev/null || true

# Build rigd for all release platforms to ./bin/{os}-{arch}/rigd
build-all:
	$(foreach p,$(PLATFORMS),\
		GOOS=$(word 1,$(subst -, ,$(p))) GOARCH=$(word 2,$(subst -, ,$(p))) CGO_ENABLED=0 \
		go build -C internal -trimpath -buildvcs=false -o ../bin/$(p)/rigd ./cmd/rigd ;)

# Build rigd, then run tests with RIG_BINARY pointing at it
test: build
	RIG_BINARY=$(CURDIR)/bin/rigd RIG_DIR=$(CURDIR)/.rig go test ./...
	cd internal && RIG_BINARY=$(CURDIR)/bin/rigd RIG_DIR=$(CURDIR)/.rig go test ./...
	cd examples && RIG_BINARY=$(CURDIR)/bin/rigd RIG_DIR=$(CURDIR)/.rig go test ./... -count=1

# Format all Go source across all modules
fmt:
	gofmt -w .

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
