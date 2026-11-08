.PHONY: web build test run dev-web image

# Build the frontend into web/dist, which the Go binary embeds.
web:
	cd web && npm ci && npm run build
	touch web/dist/.gitkeep

build: web
	go build -o bin/argo-center ./cmd/argo-center

test:
	go test ./...
	cd web && npm run typecheck

# Backend on :8080 (uses the UI embedded at last `make web`).
run:
	go run ./cmd/argo-center -config config.yaml

# Frontend with hot reload on :5173, proxying /api to `make run`.
dev-web:
	cd web && npm run dev

# Container image. Tagged releases are built and pushed to ghcr.io by .github/workflows/release.yml.
# Add BUILD_FLAGS=--network=host if the build container cannot resolve DNS.
IMAGE ?= ghcr.io/snapp-incubator/argo-center
TAG ?= dev
image:
	docker build $(BUILD_FLAGS) -t $(IMAGE):$(TAG) .
