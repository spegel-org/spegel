# Contributing

Thank you for considering contributing to Spegel, hopefully this document will make this process easier.

## Running tests

The following tools are required to run the tests properly.

* go
* golangci-lint
* kind

Run the linter and the unit tests to quickly validate changes.

```shell
make lint test
```

Run the e2e tests which take a bit more time.

```shell
make test-e2e
```

There are e2e tests for the different CNIs iptables, iptables-v6, and ipvs.

```shell
make test-e2e E2E_CNI=ipvs
```

## Building

Build the Docker image locally.

```shell
make docker-build
```

It is possible to specify a different image name and tag.

```shell
make docker-build IMG=example.com/spegel TAG=feature
```

### Local testing

In order to manually test or debug Spegel, you will need the following tools.

* kind
* docker
* helm
* kubectl

First, create a Kind cluster and a namespace for Spegel. This example uses one of the e2e test configurations. You may want to roll your own if you want to control number of nodes or node tagging.

```shell
kind create cluster --config ./test/e2e/kind-config-iptables.yaml
kubectl create namespace spegel
```

You can now build and test spegel. Note that we need to explicitly ask Kind to copy the image into the Kind cluster.

```shell
kind load docker-image example.com/spegel:feature
helm upgrade --wait --install --namespace=spegel spegel ./charts/spegel \
  --set "image.repository=example.com/spegel" \
  --set "image.tag=feature" \
  --set "nodeSelector.spegel=schedule"
kubectl --namespace spegel rollout status daemonset spegel --timeout 60s
```

If all goes well, you will see see something like `daemon set "spegel" successfully rolled out`. See [How do I know that Spegel is working?](https://spegel.dev/docs/faq/#how-do-i-know-that-spegel-is-working) on how to verify.

## Generating documentation

Changes to the Helm chart values will require the documentation to be regenerated.

```shell
make helm-docs
```

## Acceptance policy

Pull requests need to fulfill the following requirements to be accepted.

* New code has tests where applicable.
* The change has been added to the [changelog](./CHANGELOG.md).
* Documentation has been generated if applicable.
* The unit tests pass.
* Linter does not report any errors.
* All end to end tests pass.
