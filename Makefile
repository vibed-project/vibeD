GO := GOTOOLCHAIN=auto GO111MODULE=on go
BINARY := bin/vibed
KIND_CLUSTER := vibed-dev
KNATIVE_VERSION := v1.17.0

.PHONY: build run test clean setup-cluster install-knative install-deps dev teardown lint \
       test-integration test-integration-short test-integration-setup test-cleanup

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
	podman build -t vibed:dev .

load-image: image
	kind load docker-image vibed:dev --name $(KIND_CLUSTER)

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

install-deps: install-knative

dev: setup-cluster install-deps build
	@echo "Development environment ready."
	@echo "Run 'make run-http' to start vibeD"

teardown:
	kind delete cluster --name $(KIND_CLUSTER)

clean:
	rm -rf bin/
	rm -rf web/dist/
	rm -rf internal/frontend/static/assets/
	rm -rf internal/frontend/static/vite.svg
	rm -rf docs/build/
	rm -rf docs/.docusaurus/
