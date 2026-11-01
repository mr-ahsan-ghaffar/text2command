VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
PLATFORMS := darwin-arm64 darwin-amd64 linux-amd64 linux-arm64

.PHONY: build test e2e dist clean

build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/t2c ./cmd/t2c

test:
	go test ./...

# Set SHELLS to test others, for example: make e2e SHELLS="zsh bash=/path/to/bash"
SHELLS ?= zsh bash
e2e: build
	python3 test/e2e.py bin/t2c $(SHELLS)

dist:
	rm -rf dist && mkdir -p dist
	for p in $(PLATFORMS); do \
		CGO_ENABLED=0 GOOS=$${p%-*} GOARCH=$${p#*-} go build -trimpath -ldflags "$(LDFLAGS)" -o dist/t2c-$$p ./cmd/t2c || exit 1; \
	done
	cd dist && (command -v sha256sum >/dev/null && sha256sum t2c-* || shasum -a 256 t2c-*) > checksums.txt

clean:
	rm -rf bin dist
