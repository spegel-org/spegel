TAG = $$(git rev-parse --short HEAD)
IMG_NAME ?= ghcr.io/spegel-org/spegel
IMG_REF = $(IMG_NAME):$(TAG)

helm-docs:
	@cd ./charts/spegel && go tool helm-docs

lint:
	@golangci-lint run ./...

build:
	@goreleaser build --snapshot --clean --single-target --skip before

build-image: build
	@docker buildx build -t ${IMG_REF} .
	@echo ${IMG_REF}

test-unit:
	@go test ./... -race

test-integration-containerd:
	@cd ./test/integration/containerd && INTEGRATION_TEST_STRATEGY="fast" go test -v -timeout 200s -count 1 ./...

test-integration-kubernetes: build-image
	@cd ./test/integration/kubernetes && INTEGRATION_TEST_STRATEGY="fast" IMG_REF=${IMG_REF} go test -v -timeout 200s -count 1 ./...
