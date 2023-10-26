TAG = $$(git rev-parse --short HEAD)
IMG ?= ghcr.io/xenitab/spegel:$(TAG)

lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test ./...

docker-build:
	docker build -t ${IMG} .

e2e: docker-build
	./test/e2e/e2e.sh ${IMG}

BENCHMARK_CONCURRENCY = 1
BENCHMARK_IMAGE=docker.io/library/nginx@sha256:b3a676a9145dc005062d5e79b92d90574fb3bf2396f4913dc1732f9065f55c4b
benchmark-local: docker-build
	./test/benchmark/benchmark-local.sh ${IMG} ${BENCHMARK_CONCURRENCY} ${BENCHMARK_IMAGE}

tools:
	GO111MODULE=on go install github.com/norwoodj/helm-docs/cmd/helm-docs

helm-docs: tools
	cd ./charts/spegel && helm-docs
