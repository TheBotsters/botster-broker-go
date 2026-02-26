BINARY := botster-broker
GO := /usr/local/go/bin/go

.PHONY: build test run clean

build:
	$(GO) build -o $(BINARY) ./cmd/broker

test:
	$(GO) test ./...

run: build
	./$(BINARY)

clean:
	rm -f $(BINARY)
