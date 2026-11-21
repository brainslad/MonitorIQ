VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || cat VERSION)
LDFLAGS := -s -w -X main.version=$(VERSION)
DIST := dist

.PHONY: build test vet dist clean run

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/monitoriq ./cmd/monitoriq

test:
	go test ./...

vet:
	go vet ./...

# Gera pacotes .tar.gz prontos a instalar (binário + install.sh) para amd64 e arm64.
dist: clean
	@for arch in amd64 arm64; do \
		name=monitoriq-$(VERSION)-linux-$$arch; dir=$(DIST)/$$name; \
		mkdir -p $$dir/deploy; \
		CGO_ENABLED=0 GOOS=linux GOARCH=$$arch go build -trimpath -ldflags "$(LDFLAGS)" -o $$dir/monitoriq ./cmd/monitoriq || exit 1; \
		cp -r install.sh uninstall.sh README.md LICENSE VERSION $$dir/; cp -r deploy/* $$dir/deploy/; \
		tar -C $(DIST) -czf $(DIST)/$$name.tar.gz $$name; rm -rf $$dir; \
		echo "$(DIST)/$$name.tar.gz"; \
	done
	cd $(DIST) && sha256sum *.tar.gz > SHA256SUMS

run: build
	./bin/monitoriq --env ./monitoriq.env serve

clean:
	rm -rf bin $(DIST)
