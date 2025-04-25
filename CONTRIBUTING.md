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
* The change has been added to the [changelog](./CHANGELOG.md).
* Documentation has been generated if applicable.
* The unit tests pass.
* Linter does not report any errors.
* All end to end tests pass.
