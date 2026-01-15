# Contributing

Thank you for considering contributing to Spegel, hopefully this document will make this process easier.

## Running tests

The following tools are required to run the tests properly.

* go
* [golangci-lint](https://github.com/golangci/golangci-lint)
* [kind](https://github.com/kubernetes-sigs/kind)
* [goreleaser](https://github.com/goreleaser/goreleaser)
* [docker](https://docs.docker.com/get-started/get-docker/)
* [helm](https://github.com/helm/helm)
* [kubectl](https://kubernetes.io/docs/tasks/tools/#kubectl)

Run the linter and the unit tests to quickly validate changes.

```shell
make lint test-unit
```

Run the e2e tests which take a bit more time. When run locally and in PRs only the latest versions of Containerd and Kubernetes will be tested. The nightly tests will run the tests with full coverage for all supported versions.

```shell
make test-integration-containerd
make test-integration-kubernetes
```

## Building

Build the Docker image locally.

```shell
make build-image
```

It is possible to specify a different image name and tag.

```shell
make build-image IMG=example.com/spegel TAG=feature
```

### Local debugging

Run the `dev-deploy` recipe which will create a Kind cluster with the proper configuration and deploy Spegel into it. If you run this command a second time the cluster will be kept but Spegel will be updated.

```shell
make dev-deploy
```

After the command has run you can get a kubeconfig file to access the cluster and do any debugging.

```shell
kind get kubeconfig --name spegel-dev > kubeconfig
export KUBECOONFIG=$(pwd)/kubeconfig
kubectl -n spegel get pods
```

## Generate Helm documentation

Changes to the Helm chart values will require the documentation to be regenerated.

```shell
make helm-docs
```

## Acceptance policy

Pull requests need to fulfill the following requirements to be accepted.

* New code has tests where applicable.
* Linter does not report any errors.
* All unit and integration tests pass.
