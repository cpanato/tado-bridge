GO ?= go
GOLANGCI_LINT ?= golangci-lint
IMAGE ?= tado-bridge:local

.PHONY: build test vet lint vulncheck check run image image-multiarch compose-check

build:
	$(GO) build -trimpath -o bin/tado-bridge ./cmd/tado-bridge

test:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

lint:
	$(GOLANGCI_LINT) run

vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...

check: test vet lint vulncheck
	$(GO) mod verify
	$(GO) mod tidy -diff

run:
	$(GO) run ./cmd/tado-bridge serve

image:
	docker build --pull -t $(IMAGE) .

image-multiarch:
	docker buildx build --platform linux/amd64,linux/arm64 --output type=cacheonly .

compose-check:
	docker compose config --quiet

.PHONY: image-smoke
image-smoke:
	IMAGE=$(IMAGE) ./scripts/container-smoke.sh

.PHONY: trmnl-package
trmnl-package:
	mkdir -p bin
	cd integrations/trmnl && python3 -m zipfile -c ../../bin/tado-bridge-trmnl.zip settings.yml full.liquid half_horizontal.liquid half_vertical.liquid quadrant.liquid
