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

First run dev deploy which will create a Kind cluster with the proper configuration and deploy Spegel into it. If you run this command a second time the cluster will be kept but Spegel will be updated.

```shell
make dev-deploy
```

After the command has run a Kind cluster named `spegel-dev` should be created. 

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
