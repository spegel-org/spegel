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

tools:
	GO111MODULE=on go install github.com/norwoodj/helm-docs/cmd/helm-docs

helm-docs: tools
	cd ./charts/spegel && helm-docs
