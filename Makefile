SHELL := /bin/sh
CLIENTS := clients

.PHONY: web-install protocol web-build web-verify go-build build test lint deps mobile-run clean

web-install:
	cd $(CLIENTS) && npm ci

protocol:
	cd $(CLIENTS) && npm run protocol

# Builds protocol + web into a temp dir and swaps into internal/webui/web.
web-build:
	cd $(CLIENTS) && node scripts/web-build.mjs

web-verify:
	cd $(CLIENTS) && npm run typecheck && npm run lint && npm run test

go-build:
	go build ./...
	go build -o harness ./cmd/harness

build: protocol web-build go-build

test:
	cd $(CLIENTS) && npm run protocol && npm run test
	go test ./... -race

lint:
	cd $(CLIENTS) && npm run lint
	go vet ./...

deps:
	cd $(CLIENTS) && node scripts/check-deps.mjs

mobile-run:
	cd $(CLIENTS)/apps/mobile && npx expo start

clean:
	rm -f harness harness.exe
	rm -rf $(CLIENTS)/packages/*/dist $(CLIENTS)/apps/web/.web-build-tmp
