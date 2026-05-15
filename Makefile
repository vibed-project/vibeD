GO := GOTOOLCHAIN=auto GO111MODULE=on go
BINARY := bin/vibed
KIND_CLUSTER := vibed-dev
KIND_RUNTIME := podman
GHCR_IMAGE := ghcr.io/vibed-project/vibed
RUNNER_PYTHON_IMAGE := localhost/vibed-runner-python:dev
RUNNER_NODE_IMAGE := localhost/vibed-runner-node:dev

# Code generation
CONTROLLER_GEN_VERSION := v0.18.0
CONTROLLER_GEN := $(shell pwd)/bin/controller-gen
OAPI_CODEGEN_VERSION := v2.4.1
OAPI_CODEGEN := $(shell pwd)/bin/oapi-codegen

# Testbed paths (cluster + shared-infra helm charts)
TESTBED := testbed
TB_KIND := $(TESTBED)/kind-cluster
TB_REGISTRY := $(TESTBED)/kind-registry
TB_KNATIVE := $(TESTBED)/knative
TB_OBS := $(TESTBED)/observability
TB_KEYCLOAK := $(TESTBED)/keycloak
TB_AGENT_SANDBOX := $(TESTBED)/agent-sandbox

.PHONY: build run run-http web-install web-build docs-install docs-build docs-dev build-all \
        test test-integration test-integration-short test-integration-setup test-cleanup lint \
        generate manifests controller-gen openapi-gen oapi-codegen \
        image load-image \
        runner-images runner-image-python runner-image-node load-runner-images \
        setup-cluster install-registry install-knative install-observability install-keycloak \
        install-agent-sandbox install-deps install-vibed install-vibed-fastpath \
        dev dev-status run-latest teardown clean

## Build

build:
	$(GO) build -o $(BINARY) ./cmd/vibed

run: build
	./$(BINARY) --config vibed.yaml

run-http: build
	./$(BINARY) --config vibed.yaml --transport http

## Frontend

web-install:
	cd web && npm install

web-build:
	cd web && npm run build

## Documentation

docs-install:
	cd docs && npm install

docs-build:
	cd docs && npx docusaurus build

docs-dev:
	cd docs && npx docusaurus start

## Full build (frontend + backend)

build-all: web-build build

## Test

test:
	$(GO) test ./...

test-integration-setup:
	@echo "Loading test images into Kind cluster..."
	podman pull docker.io/library/nginx:1.27-alpine 2>/dev/null || true
	kind load docker-image docker.io/library/nginx:1.27-alpine --name $(KIND_CLUSTER) 2>/dev/null || true
	podman pull docker.io/nginxinc/nginx-unprivileged:alpine 2>/dev/null || true
	kind load docker-image docker.io/nginxinc/nginx-unprivileged:alpine --name $(KIND_CLUSTER) 2>/dev/null || true

test-integration: test-integration-setup
	$(GO) test -tags=integration -timeout 10m -count=1 -v ./...

test-integration-short: test-integration-setup
	$(GO) test -tags=integration -short -timeout 5m -count=1 -v ./...

test-cleanup:
	kubectl delete ns -l vibed-test=true --ignore-not-found

lint:
	golangci-lint run ./...

## Code generation (CRDs + DeepCopy)

# Installs controller-gen into ./bin if missing. Pinned via CONTROLLER_GEN_VERSION.
controller-gen:
	@test -x $(CONTROLLER_GEN) || GOBIN=$(shell pwd)/bin $(GO) install sigs.k8s.io/controller-tools/cmd/controller-gen@$(CONTROLLER_GEN_VERSION)

# Regenerates DeepCopy methods for all kubebuilder-annotated API packages.
# GO111MODULE=on + GOTOOLCHAIN=auto propagate to the go invocation controller-gen
# does internally when it loads packages.
generate: controller-gen
	GOTOOLCHAIN=auto GO111MODULE=on $(CONTROLLER_GEN) object paths="./pkg/vibedapi/..."

# Regenerates CRD YAML from kubebuilder markers. Output is committed; do not
# hand-edit crds/*.yaml — re-run `make manifests` instead.
manifests: controller-gen
	GOTOOLCHAIN=auto GO111MODULE=on $(CONTROLLER_GEN) crd paths="./pkg/vibedapi/..." output:crd:dir=crds

# Installs oapi-codegen into ./bin if missing. Pinned via OAPI_CODEGEN_VERSION.
oapi-codegen:
	@test -x $(OAPI_CODEGEN) || GOBIN=$(shell pwd)/bin $(GO) install github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@$(OAPI_CODEGEN_VERSION)

# Regenerates Go types + std-http server stubs from api/openapi.yaml. Output
# goes to pkg/vibedapi/http/*.gen.go. Generated; do not hand-edit.
openapi-gen: oapi-codegen
	GOTOOLCHAIN=auto GO111MODULE=on $(OAPI_CODEGEN) -config api/oapi-codegen-types.yaml  api/openapi.yaml
	GOTOOLCHAIN=auto GO111MODULE=on $(OAPI_CODEGEN) -config api/oapi-codegen-server.yaml api/openapi.yaml

## Container

image:
	podman build -t localhost/vibed:dev .

load-image: image
	podman save localhost/vibed:dev -o /tmp/vibed-dev.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-dev.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-dev.tar

## Runner Images (Instant Preview fast path — see FAST-PATH.md)
## Build context is the repo root: the Dockerfiles compile the runner agent
## from the Go source tree. Built once, not per request.

runner-images: runner-image-python runner-image-node

runner-image-python:
	podman build -f runners/python/Dockerfile -t $(RUNNER_PYTHON_IMAGE) .

runner-image-node:
	podman build -f runners/node/Dockerfile -t $(RUNNER_NODE_IMAGE) .

load-runner-images: runner-images
	podman save $(RUNNER_PYTHON_IMAGE) -o /tmp/vibed-runner-python.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-runner-python.tar --name $(KIND_CLUSTER)
	podman save $(RUNNER_NODE_IMAGE) -o /tmp/vibed-runner-node.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-runner-node.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-runner-python.tar /tmp/vibed-runner-node.tar

## Local Dev Environment (delegates to testbed/)

setup-cluster:
	$(TB_KIND)/bootstrap.sh --cluster $(KIND_CLUSTER) --runtime $(KIND_RUNTIME)

install-registry:
	helm upgrade --install kind-registry $(TB_REGISTRY)/ \
		--set 'aliases[0].namespace=vibed-system' \
		--set 'aliases[0].createNamespace=true' \
		--wait
	$(TB_REGISTRY)/scripts/configure-containerd.sh \
		--cluster $(KIND_CLUSTER) --runtime $(KIND_RUNTIME)

install-knative:
	helm upgrade --install knative $(TB_KNATIVE)/ \
		--namespace knative-system --create-namespace \
		--wait --timeout 10m

install-observability:
	helm dependency build $(TB_OBS)/
	helm upgrade --install observability $(TB_OBS)/ \
		--namespace monitoring --create-namespace \
		--wait --timeout 10m

install-keycloak:
	helm upgrade --install keycloak $(TB_KEYCLOAK)/ \
		--namespace vibed-system --create-namespace \
		--wait --timeout 5m

install-agent-sandbox:
	helm upgrade --install agent-sandbox $(TB_AGENT_SANDBOX)/ \
		--namespace agent-sandbox-system --create-namespace \
		--wait --timeout 5m

install-deps: install-registry install-knative install-keycloak install-agent-sandbox

install-vibed: load-image
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		--set image.repository=localhost/vibed --set image.tag=dev --set image.pullPolicy=Never \
		--set service.type=NodePort --set service.nodePort=31808 \
		--set config.server.baseURL=http://localhost:31808 \
		--set config.store.backend=sqlite \
		--set config.store.sqlite.path=/data/vibed/vibed.db \
		--set config.tracing.enabled=true \
		--set config.tracing.endpoint=observability-opentelemetry-collector.monitoring:4317 \
		--wait

## install-vibed-fastpath: same as install-vibed, plus the Instant Preview fast
## path enabled with locally built runner images (loaded into Kind), runners in
## a dedicated `vibed-runners` namespace.
install-vibed-fastpath: load-image load-runner-images
	@kubectl create namespace vibed-runners --dry-run=client -o yaml | kubectl apply -f -
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		--set image.repository=localhost/vibed --set image.tag=dev --set image.pullPolicy=Never \
		--set service.type=NodePort --set service.nodePort=31808 \
		--set config.server.baseURL=http://localhost:31808 \
		--set config.store.backend=sqlite \
		--set config.store.sqlite.path=/data/vibed/vibed.db \
		--set config.tracing.enabled=true \
		--set config.tracing.endpoint=observability-opentelemetry-collector.monitoring:4317 \
		--set config.fastPath.enabled=true \
		--set config.fastPath.namespace=vibed-runners \
		--set 'config.fastPath.runners.python.image=localhost/vibed-runner-python:dev' \
		--set config.fastPath.runners.python.poolSize=2 \
		--set 'config.fastPath.runners.nodejs.image=localhost/vibed-runner-node:dev' \
		--set config.fastPath.runners.nodejs.poolSize=2 \
		--wait

dev: setup-cluster install-deps install-observability install-vibed
	@echo ""
	@echo "============================================"
	@echo "  vibeD development environment is ready!"
	@echo "============================================"
	@$(MAKE) dev-status

dev-status:
	@echo ""
	@echo "=== Pods ==="
	@kubectl get pods -n vibed-system 2>/dev/null || true
	@kubectl get pods -n monitoring 2>/dev/null || true
	@kubectl get pods -n knative-serving 2>/dev/null || true
	@kubectl get pods -n agent-sandbox-system 2>/dev/null || true
	@echo ""
	@echo "=== URLs ==="
	@echo "  vibeD Dashboard:  http://localhost:8080"
	@echo "  vibeD API:        http://localhost:8080/api/artifacts"
	@echo "  Swagger UI:       http://localhost:8080/api/docs/"
	@echo "  Knative Apps:     http://<app>.localhost (port 80)"
	@echo "  Keycloak:         http://localhost:31888  (admin / admin)"
	@echo "  Grafana:          http://localhost:3000  (admin / admin)"
	@echo "  Prometheus:       http://localhost:9090"
	@echo "  Explore Traces:   Grafana -> Explore -> Tempo"
	@echo "  Explore Logs:     Grafana -> Explore -> Loki"
	@echo ""

run-latest:
	podman pull $(GHCR_IMAGE):latest
	podman save $(GHCR_IMAGE):latest -o /tmp/vibed-latest.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-latest.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-latest.tar
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		--set image.repository=$(GHCR_IMAGE) --set image.tag=latest --set image.pullPolicy=Never \
		--set service.type=NodePort --set service.nodePort=31808 \
		--set metrics.serviceMonitor.enabled=true \
		--set config.server.baseURL=http://localhost:31808 \
		--wait
	@$(MAKE) dev-status

teardown:
	$(TB_KIND)/teardown.sh --cluster $(KIND_CLUSTER) --runtime $(KIND_RUNTIME)

clean:
	rm -rf bin/
	rm -rf web/dist/
	rm -rf internal/frontend/static/assets/
	rm -rf internal/frontend/static/vite.svg
	rm -rf docs/build/
	rm -rf docs/.docusaurus/
