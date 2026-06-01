GO := GOTOOLCHAIN=auto GO111MODULE=on go
BINARY := bin/vibed
CONTROLLER_BINARY := bin/vibed-controller
ROUTER_BINARY := bin/vibed-router
WORKERD_LOADER_BINARY := bin/vibed-workerd-loader
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
TB_OBS := $(TESTBED)/observability
TB_KEYCLOAK := $(TESTBED)/keycloak
TB_AGENT_SANDBOX := $(TESTBED)/agent-sandbox

.PHONY: build build-controller build-router build-workerd-loader run run-http web-install web-build docs-install docs-build docs-dev build-all \
        test e2e e2e-cluster test-integration test-integration-short test-integration-setup test-cleanup lint \
        generate manifests controller-gen openapi-gen oapi-codegen \
        image load-image \
        runner-images runner-image-python runner-image-node load-runner-images \
        setup-cluster install-registry install-observability install-keycloak \
        install-agent-sandbox install-deps install-vibed \
        dev dev-status run-latest teardown clean

## Build

build:
	$(GO) build -o $(BINARY) ./cmd/vibed

build-controller:
	$(GO) build -o $(CONTROLLER_BINARY) ./cmd/vibed-controller

build-router:
	$(GO) build -o $(ROUTER_BINARY) ./cmd/vibed-router

build-workerd-loader:
	$(GO) build -o $(WORKERD_LOADER_BINARY) ./cmd/vibed-workerd-loader

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

## e2e: in-process end-to-end across the implemented slice (no cluster needed).
e2e:
	$(GO) test -count=1 ./test/e2e/...

## e2e-cluster: the literal §10.2 smoke test; needs a cluster with the chart
## installed (skips otherwise). See test/e2e/README.md.
e2e-cluster:
	$(GO) test -tags=e2ecluster -count=1 -v ./test/e2e/...

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

# Controller + router image builds. install-vibed depends on these because
# their CLI flag surface evolves with the chart — using a stale GHCR image
# (e.g. v0.3.1 controller after a flag was added) crashes the deployment.
image-controller:
	podman build -t localhost/vibed-controller:dev -f cmd/vibed-controller/Dockerfile .

load-controller-image: image-controller
	podman save localhost/vibed-controller:dev -o /tmp/vibed-controller-dev.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-controller-dev.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-controller-dev.tar

image-router:
	podman build -t localhost/vibed-router:dev -f cmd/vibed-router/Dockerfile .

load-router-image: image-router
	podman save localhost/vibed-router:dev -o /tmp/vibed-router-dev.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-router-dev.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-router-dev.tar

# Static-nginx template image: the only warm-pool image values-kind.yaml
# enables, so install-vibed needs it loaded or no claim ever binds.
image-static-nginx:
	podman build -t localhost/vibed-template-static-nginx:dev -f templates/static-nginx/Dockerfile .

load-static-nginx-image: image-static-nginx
	podman save localhost/vibed-template-static-nginx:dev -o /tmp/vibed-template-static-nginx-dev.tar
	KIND_EXPERIMENTAL_PROVIDER=$(KIND_RUNTIME) kind load image-archive /tmp/vibed-template-static-nginx-dev.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-template-static-nginx-dev.tar

## Opt-in warm pools beyond static-nginx. install-vibed enables only the
## static pool by default (the kind overlay disables the rest so the dev
## install stays fast and small). These targets build + load the slot's
## image and helm-upgrade to flip the pool on without changing the chart.
enable-python-pool: load-runner-images
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		-f deploy/helm/vibed/values-kind.yaml \
		--reuse-values \
		--set warmPools.python-313.enabled=true \
		--set warmPools.python-313.lane=general \
		--set warmPools.python-313.image=$(RUNNER_PYTHON_IMAGE) \
		--set warmPools.python-313.replicas=1 \
		--wait --timeout 3m
	@kubectl rollout restart -n vibed-system deploy/vibed-controller
	@echo "python-313 pool enabled. The validator re-checks on controller restart; first deploy may need ~10s."

enable-node-pool: load-runner-images
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		-f deploy/helm/vibed/values-kind.yaml \
		--reuse-values \
		--set warmPools.node-24.enabled=true \
		--set warmPools.node-24.lane=general \
		--set warmPools.node-24.image=$(RUNNER_NODE_IMAGE) \
		--set warmPools.node-24.replicas=1 \
		--wait --timeout 3m
	@kubectl rollout restart -n vibed-system deploy/vibed-controller
	@echo "node-24 pool enabled. The validator re-checks on controller restart; first deploy may need ~10s."

## Runner Images (Instant Preview fast path — see FAST-PATH.md)
## Build context is the repo root: the Dockerfiles compile the runner agent
## from the Go source tree. Built once, not per request.

runner-images: runner-image-python runner-image-node

runner-image-python:
	podman build -f templates/python-313/Dockerfile -t $(RUNNER_PYTHON_IMAGE) .

runner-image-node:
	podman build -f templates/node-24/Dockerfile -t $(RUNNER_NODE_IMAGE) .

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

install-deps: install-registry install-keycloak install-agent-sandbox

install-vibed: load-image load-controller-image load-router-image load-static-nginx-image
	helm upgrade --install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		-f deploy/helm/vibed/values-kind.yaml \
		--set config.tracing.enabled=true \
		--set config.tracing.endpoint=observability-opentelemetry-collector.monitoring:4317 \
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
	@kubectl get pods -n vibed-pools 2>/dev/null || true
	@kubectl get pods -n agent-sandbox-system 2>/dev/null || true
	@echo ""
	@echo "=== URLs ==="
	@echo "  vibeD Dashboard:  http://localhost:8080"
	@echo "  vibeD API:        http://localhost:8080/api/artifacts"
	@echo "  Swagger UI:       http://localhost:8080/api/docs/"
	@echo "  Deployed Apps:    http://<app>.localhost (port 80)"
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
