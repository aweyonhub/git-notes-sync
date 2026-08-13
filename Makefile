BIN := notes-sync
VERSION ?= 0.1.0
LDFLAGS := -s -w

.PHONY: build test vet cross clean install

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o $(BIN) .

test:
	go test ./...

vet:
	go vet ./...

# Cross-compile all release binaries into dist/
cross:
	mkdir -p dist
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/notes-sync-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/notes-sync-linux-arm64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/notes-sync-darwin-amd64 .
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/notes-sync-darwin-arm64 .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/notes-sync-windows-amd64.exe .

clean:
	rm -rf dist $(BIN)
