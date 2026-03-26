BINARY := botster-broker
PROXY_BINARY := ws-proxy
GO := /usr/local/go/bin/go

.PHONY: build build-proxy build-all test run run-split clean install-split

# Single-process mode (default, backward compatible).
build:
	$(GO) build -o $(BINARY) ./cmd/broker

# Build the WebSocket proxy for two-process mode.
build-proxy:
	$(GO) build -o $(PROXY_BINARY) ./cmd/ws-proxy

# Build both binaries.
build-all: build build-proxy

test:
	$(GO) test ./...

# Single-process mode.
run: build
	./$(BINARY)

# Two-process mode: start proxy first, then broker with PROXY_SOCKET.
run-split: build-all
	@echo "Starting ws-proxy..."
	./$(PROXY_BINARY) -socket /tmp/hub.sock -listen :9084 &
	@sleep 1
	@echo "Starting broker in link mode..."
	PROXY_SOCKET=/tmp/hub.sock ./$(BINARY)

clean:
	rm -f $(BINARY) $(PROXY_BINARY)

# Install both binaries to /usr/local/bin.
install-split: build-all
	install -m 755 $(BINARY) /usr/local/bin/$(BINARY)
	install -m 755 $(PROXY_BINARY) /usr/local/bin/$(PROXY_BINARY)
