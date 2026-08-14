BIN := gns
VERSION ?= 0.1.1
GIT_COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)

PKG := github.com/aweyonhub/git-notes-sync
LDFLAGS := -s -w \
	-X $(PKG)/internal/version.Version=$(VERSION) \
	-X $(PKG)/internal/version.Commit=$(GIT_COMMIT)

.PHONY: build test vet fmt cross clean install

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) ./cmd/gns

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -l -w cmd internal

install:
	go install -trimpath -ldflags "$(LDFLAGS)" ./cmd/gns

# Cross-compile all release binaries into dist/
cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/gns-linux-amd64 ./cmd/gns
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/gns-linux-arm64 ./cmd/gns
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/gns-darwin-amd64 ./cmd/gns
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/gns-darwin-arm64 ./cmd/gns
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/gns-windows-amd64.exe ./cmd/gns

clean:
	rm -rf dist $(BIN)
