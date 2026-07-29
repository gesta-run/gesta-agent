.PHONY: verify test build install install-codex

verify: test build

test:
	go test ./...

build:
	mkdir -p bin
	go build -o bin/gesta-agent ./cmd/gesta-agent

install:
	@if [ -z "$(CONTROL_URL)" ] || [ -z "$(APIKEY)" ]; then \
		echo "usage: make install CONTROL_URL=http://127.0.0.1:8080 APIKEY=sk-..."; \
		exit 2; \
	fi
	./scripts/install.sh --control-url "$(CONTROL_URL)" --apikey "$(APIKEY)"

install-codex: install
