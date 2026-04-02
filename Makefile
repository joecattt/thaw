VERSION := $(shell grep 'var version' cmd/thaw/main.go | head -1 | cut -d'"' -f2)
BINARY := thaw
GOFLAGS := -ldflags="-s -w"
PREFIX := /usr/local/bin

.PHONY: build install test vet clean release version

build:
	CGO_ENABLED=1 go build $(GOFLAGS) -o $(BINARY) ./cmd/thaw

install: build
	sudo cp $(BINARY) $(PREFIX)/$(BINARY)
	@echo "Installed thaw $(VERSION) to $(PREFIX)/$(BINARY)"
	@echo "Run: thaw setup"

test:
	CGO_ENABLED=1 go test ./... -count=1

vet:
	go vet ./...

clean:
	rm -f $(BINARY)

release: clean
	@mkdir -p dist
	GOOS=darwin GOARCH=arm64 CGO_ENABLED=1 go build $(GOFLAGS) -o dist/thaw-darwin-arm64 ./cmd/thaw
	GOOS=darwin GOARCH=amd64 CGO_ENABLED=1 go build $(GOFLAGS) -o dist/thaw-darwin-amd64 ./cmd/thaw
	GOOS=linux GOARCH=amd64 CGO_ENABLED=1 go build $(GOFLAGS) -o dist/thaw-linux-amd64 ./cmd/thaw
	@echo "Built $(VERSION) in dist/"

uninstall:
	sudo rm -f $(PREFIX)/$(BINARY)

version:
	@echo $(VERSION)
