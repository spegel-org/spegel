TAG = $$(git rev-parse --short HEAD)
IMG ?= ghcr.io/spegel-org/spegel
REF = $(IMG):$(TAG)
CNI ?= iptables
DELETE_E2E_CLUSTER ?= true

lint:
	golangci-lint run ./...

.PHONY: test
test:
	go test ./...

docker-build:
	docker build -t ${REF} .

e2e: docker-build
	./test/e2e/e2e.sh ${REF} ${CNI} ${DELETE_E2E_CLUSTER}

tools:
	GO111MODULE=on go install github.com/norwoodj/helm-docs/cmd/helm-docs

helm-docs: tools
	cd ./charts/spegel && helm-docs
