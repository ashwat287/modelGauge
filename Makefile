GO ?= go
PREFIX ?= $(HOME)/.local
BINDIR ?= $(PREFIX)/bin

.PHONY: build runtimes test install

build: runtimes
	$(GO) build -tags bundled -trimpath -o bin/modelgauge ./cmd/modelgauge

runtimes:
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 $(GO) build -tags '' -trimpath -ldflags='-s -w' -o internal/control/runtimes/linux-arm64 ./cmd/modelgauge
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 $(GO) build -tags '' -trimpath -ldflags='-s -w' -o internal/control/runtimes/linux-amd64 ./cmd/modelgauge
	CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 $(GO) build -tags remotehost -trimpath -ldflags='-s -w' -o internal/control/runtimes/darwin-amd64 ./cmd/modelgauge
	CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 $(GO) build -tags remotehost -trimpath -ldflags='-s -w' -o internal/control/runtimes/darwin-arm64 ./cmd/modelgauge

test: build
	MODELGAUGE_TEST_INSTALLED_CLI="$(CURDIR)/bin/modelgauge" $(GO) test -tags bundled -race ./...
	$(GO) vet -tags bundled ./...

install: build
	install -d "$(BINDIR)"
	install -m 755 bin/modelgauge "$(BINDIR)/modelgauge"
	@printf 'Installed: %s/modelgauge\n' "$(BINDIR)"
	@case ":$$PATH:" in *":$(BINDIR):"*) ;; *) printf 'Add to your shell profile: export PATH="%s:$$PATH"\n' "$(BINDIR)" ;; esac
