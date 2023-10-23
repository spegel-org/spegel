TAG = $$(git rev-parse --short HEAD)
IMG ?= ghcr.io/xenitab/spegel:$(TAG)

lint:
	golangci-lint run ./...

test:
	go test ./...

e2e: docker-build
	./test/e2e/e2e.sh ${IMG}

docker-build:
	docker build -t ${IMG} .

tools:
	GO111MODULE=on go install github.com/norwoodj/helm-docs/cmd/helm-docs

helm-docs: tools
	cd ./charts/spegel && helm-docs
