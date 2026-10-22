GO ?= go
GOLANGCI_LINT ?= golangci-lint
IMAGE ?= $(or $(TADO_BRIDGE_IMAGE),ko.local/tado-bridge:local)
COMPOSE ?= docker compose
KO ?= ko
GORELEASER ?= goreleaser
HELM ?= helm
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
PLATFORM ?= linux/$(shell $(GO) env GOARCH)

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
	KO_DOCKER_REPO=ko.local/tado-bridge $(KO) build --bare --platform $(PLATFORM) --tags local --image-user=65532:65532 --image-label "org.opencontainers.image.created=$(BUILD_DATE)" ./cmd/tado-bridge
	docker tag ko.local/tado-bridge:local $(IMAGE)

image-multiarch:
	mkdir -p dist
	KO_DOCKER_REPO=example.invalid/tado-bridge $(KO) build --bare --push=false --platform linux/amd64,linux/arm64 --image-user=65532:65532 --image-label "org.opencontainers.image.created=$(BUILD_DATE)" --oci-layout-path dist/oci ./cmd/tado-bridge

.PHONY: release-check release-snapshot helm-check helm-package

release-check:
	$(GORELEASER) check

release-snapshot:
	$(GORELEASER) release --snapshot --clean --skip=publish,sign

helm-check:
	$(HELM) lint --strict charts/tado-bridge
	$(HELM) template local charts/tado-bridge > /dev/null
	$(GO) test ./internal/packaging

helm-package:
	mkdir -p dist/charts
	$(HELM) package charts/tado-bridge --destination dist/charts

compose-check:
	TADO_BRIDGE_IMAGE="$(IMAGE)" $(COMPOSE) config --quiet

.PHONY: compose-up compose-down
compose-up: compose-check image
	TADO_BRIDGE_IMAGE="$(IMAGE)" $(COMPOSE) up

compose-down:
	TADO_BRIDGE_IMAGE="$(IMAGE)" $(COMPOSE) down

.PHONY: image-smoke
image-smoke:
	IMAGE=$(IMAGE) ./scripts/container-smoke.sh

.PHONY: trmnl-package
trmnl-package:
	mkdir -p bin
	cd integrations/trmnl && python3 -m zipfile -c ../../bin/tado-bridge-trmnl.zip settings.yml full.liquid half_horizontal.liquid half_vertical.liquid quadrant.liquid

.PHONY: compose-smoke
compose-smoke:
	IMAGE=$(IMAGE) ./scripts/compose-smoke.sh

.PHONY: chart-integration
chart-integration:
	./scripts/chart-integration.sh

.PHONY: observability-smoke observability-screenshot
observability-smoke:
	python3 scripts/observability-smoke.py

observability-screenshot:
	python3 scripts/observability-smoke.py --screenshot docs/images/grafana/dashboard.png
