GO := GOTOOLCHAIN=auto GO111MODULE=on go
BINARY := bin/vibed
KIND_CLUSTER := vibed-dev
KNATIVE_VERSION := v1.17.0

.PHONY: build run test clean setup-cluster install-knative setup-registry install-deps dev teardown lint \
       test-integration test-integration-short test-integration-setup test-cleanup image load-image \
       install-observability install-vibed dev-status

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

test-integration: test-integration-setup
	$(GO) test -tags=integration -timeout 10m -count=1 -v ./...

test-integration-short: test-integration-setup
	$(GO) test -tags=integration -short -timeout 5m -count=1 -v ./...

test-cleanup:
	kubectl delete ns -l vibed-test=true --ignore-not-found

lint:
	golangci-lint run ./...

## Container

image:
	podman build -t localhost/vibed:dev .

load-image: image
	podman save localhost/vibed:dev -o /tmp/vibed-dev.tar
	KIND_EXPERIMENTAL_PROVIDER=podman kind load image-archive /tmp/vibed-dev.tar --name $(KIND_CLUSTER)
	@rm -f /tmp/vibed-dev.tar

## Local Dev Environment

setup-cluster:
	KIND_EXPERIMENTAL_PROVIDER=podman kind create cluster \
		--name $(KIND_CLUSTER) \
		--config deploy/kind/kind-config.yaml

install-knative:
	kubectl apply -f https://github.com/knative/serving/releases/download/knative-$(KNATIVE_VERSION)/serving-crds.yaml
	kubectl apply -f https://github.com/knative/serving/releases/download/knative-$(KNATIVE_VERSION)/serving-core.yaml
	kubectl wait --for=condition=Available deployment --all -n knative-serving --timeout=120s
	kubectl apply -f https://github.com/knative/net-kourier/releases/download/knative-$(KNATIVE_VERSION)/kourier.yaml
	kubectl wait --for=condition=Available deployment --all -n kourier-system --timeout=120s
	kubectl patch configmap/config-network -n knative-serving \
		--type merge -p '{"data":{"ingress-class":"kourier.ingress.networking.knative.dev"}}'
	kubectl patch configmap/config-domain -n knative-serving \
		--type merge -p '{"data":{"127.0.0.1.sslip.io":""}}'
	kubectl patch service kourier -n kourier-system \
		--type merge -p '{"spec":{"type":"NodePort","ports":[{"name":"http2","port":80,"targetPort":8080,"nodePort":31080,"protocol":"TCP"}]}}'
	kubectl patch configmap/config-deployment -n knative-serving \
		--type merge -p '{"data":{"registries-skipping-tag-resolving":"kind-registry:5000"}}'

setup-registry:
	kubectl apply -f deploy/kind/registry.yaml
	kubectl wait --for=condition=Available deployment/kind-registry -n default --timeout=60s
	@echo "Configuring containerd registry mirror for kind-registry:5000..."
	@REGISTRY_IP=$$(kubectl get svc kind-registry -n default -o jsonpath='{.spec.clusterIP}') && \
		docker exec $(KIND_CLUSTER)-control-plane mkdir -p /etc/containerd/certs.d/kind-registry:5000 && \
		docker exec $(KIND_CLUSTER)-control-plane sh -c "printf '[host.\"http://'$$REGISTRY_IP':5000\"]\n  capabilities = [\"pull\", \"resolve\"]\n  skip_verify = true\n' > /etc/containerd/certs.d/kind-registry:5000/hosts.toml"
	kubectl create namespace vibed-system --dry-run=client -o yaml | kubectl apply -f -
	kubectl apply -f deploy/kind/registry-external.yaml

install-deps: install-knative setup-registry

install-observability:
	helm dependency build deploy/helm/vibed-observability/
	helm install vibed-observability deploy/helm/vibed-observability/ \
		--namespace monitoring --create-namespace --wait --timeout 10m

install-vibed: load-image
	helm install vibed deploy/helm/vibed/ \
		--namespace vibed-system --create-namespace \
		--set image.repository=localhost/vibed --set image.tag=dev --set image.pullPolicy=Never \
		--set service.type=NodePort --set service.nodePort=31808 \
		--set metrics.serviceMonitor.enabled=true --wait

dev: setup-cluster install-deps install-observability install-vibed
	@echo ""
	@echo "============================================"
	@echo "  vibeD development environment is ready!"
	@echo "============================================"
	@$(MAKE) dev-status

dev-status:
	@echo ""
	@echo "=== Pods ==="
	@kubectl get pods -n vibed-system
	@kubectl get pods -n monitoring -l 'app.kubernetes.io/name in (prometheus,grafana)'
	@echo ""
	@echo "=== URLs ==="
	@echo "  vibeD Dashboard:  http://localhost:8080"
	@echo "  vibeD API:        http://localhost:8080/api/artifacts"
	@echo "  Swagger UI:       http://localhost:8080/api/docs/"
	@echo "  Knative Apps:     http://<app>.127.0.0.1.sslip.io (port 80)"
	@echo "  Grafana:          http://localhost:3000  (admin / vibed-dev)"
	@echo "  Prometheus:       http://localhost:9090"
	@echo ""

teardown:
	kind delete cluster --name $(KIND_CLUSTER)

clean:
	rm -rf bin/
	rm -rf web/dist/
	rm -rf internal/frontend/static/assets/
	rm -rf internal/frontend/static/vite.svg
	rm -rf docs/build/
	rm -rf docs/.docusaurus/
