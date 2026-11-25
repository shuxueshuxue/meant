GO ?= go
LDFLAGS = -s -w
TARGETS = linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64

build:
	$(GO) build -ldflags "$(LDFLAGS)" -o bin/meant ./cmd/meant

test: build
	$(GO) vet ./...
	$(GO) test ./internal/...
	test/incidents.sh

# One static binary per platform; no cgo anywhere.
dist:
	@for t in $(TARGETS); do \
	  os=$${t%/*}; arch=$${t#*/}; out=dist/meant-$$os-$$arch; [ $$os = windows ] && out=$$out.exe; \
	  CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch $(GO) build -ldflags "$(LDFLAGS)" -o $$out ./cmd/meant && echo $$out; \
	done

.PHONY: build test dist
