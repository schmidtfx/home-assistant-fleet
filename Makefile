.PHONY: all web build test test-go test-web dev dev-backend dev-web fakeha proxmox clean

BIN := bin/hafleet
DEV_KEY_FILE := data/dev.key
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PROXMOX_ARCH ?= amd64

all: build

web/node_modules: web/package-lock.json
	cd web && npm ci
	@touch $@

web: web/node_modules
	cd web && npm run build

build: web
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN) ./cmd/hafleet

test: test-go test-web

test-go:
	go vet ./...
	go test -race ./...

test-web: web/node_modules
	cd web && npm run typecheck && npm test

$(DEV_KEY_FILE):
	@mkdir -p data
	go run ./cmd/hafleet genkey > $@

# Backend on :8080 (plain HTTP, non-Secure cookies) + Vite on :5173 with /api proxied.
dev: $(DEV_KEY_FILE) web/node_modules
	@$(MAKE) -j2 dev-backend dev-web

dev-backend: $(DEV_KEY_FILE)
	HAFLEET_SECRET_KEY_FILE=$(DEV_KEY_FILE) go run ./cmd/hafleet serve -dev -secure-cookies=false \
		-allowed-origins http://localhost:5173 -listen 127.0.0.1:8080

dev-web:
	cd web && npm run dev

fakeha:
	go run ./cmd/fakeha

# Proxmox LXC template (Alpine + hafleet as an OpenRC service) in dist/.
# PROXMOX_ARCH=amd64|arm64. Upload to a storage's "CT Templates", then create a container from it.
proxmox: web
	CGO_ENABLED=0 GOOS=linux GOARCH=$(PROXMOX_ARCH) go build -trimpath -ldflags="-s -w" \
		-o build/proxmox/hafleet-linux-$(PROXMOX_ARCH) ./cmd/hafleet
	sh scripts/build-proxmox-template.sh $(PROXMOX_ARCH) build/proxmox/hafleet-linux-$(PROXMOX_ARCH) dist $(VERSION)

clean:
	rm -rf bin build dist web/dist/assets web/dist/index.html web/dist/favicon.svg
