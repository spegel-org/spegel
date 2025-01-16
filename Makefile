TAG = $$(git rev-parse --short HEAD)
IMG_NAME ?= ghcr.io/spegel-org/spegel
IMG_REF = $(IMG_NAME):$(TAG)
E2E_PROXY_MODE ?= iptables
E2E_IP_FAMILY ?= ipv4

lint:
	golangci-lint run ./...

docker-build:
	docker build -t ${IMG_REF} .

test-unit:
	go test ./...

test-e2e: docker-build
	IMG_REF=${IMG_REF} \
	E2E_PROXY_MODE=${E2E_PROXY_MODE} \
	E2E_IP_FAMILY=${E2E_IP_FAMILY} \
	go test ./test/e2e -v -timeout 200s -tags e2e

tools:
	GO111MODULE=on go install github.com/norwoodj/helm-docs/cmd/helm-docs

helm-docs: tools
	cd ./charts/spegel && helm-docs
