TAG = $$(git rev-parse --short HEAD)
IMG_NAME ?= ghcr.io/spegel-org/spegel
IMG_REF = $(IMG_NAME):$(TAG)

.PHONY: generate
generate: charts/spegel/README.md

charts/spegel/README.md: charts/spegel/values.yaml charts/spegel/README.md.gotmpl
	@cd ./charts/spegel && go tool helm-docs

.PHONY: lint
lint:
	@golangci-lint run ./...

.PHONY: build
build:
	@goreleaser build --snapshot --clean --single-target --skip before

.PHONY: build-image
build-image: build
	@docker buildx build -t ${IMG_REF} .
	@echo ${IMG_REF}

.PHONY: test-unit
test-unit:
	@go test ./... -race

.PHONY: test-integration-containerd
test-integration-containerd:
	@cd ./test/integration/containerd && INTEGRATION_TEST_STRATEGY="fast" go test -v -timeout 200s -count 1 ./...

.PHONY: test-integration-kubernetes
test-integration-kubernetes: build-image
	@cd ./test/integration/kubernetes && INTEGRATION_TEST_STRATEGY="fast" IMG_REF=${IMG_REF} go test -v -timeout 300s -count 1 ./...
