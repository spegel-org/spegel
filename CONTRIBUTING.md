# Contributing

Thank you for considering contributing to Spegel, hopefully this document will make this process easier.

## AI Policy

Spegel has an [AI Policy](./AI_POLICY.md) please read it first before going any further.

## Acceptance Policy

Before creating a pull request (PR) make sure that there is a related issue to the change. Fly-by PRs without prior discussions are generally not appreciated unless they are minor fixes like typos. Something that you may consider a simple bug fix or feature may in fact be a limitation of the project or already considered out of scope. It is a lot easier to review a PR with a previously discussed solution making changes faster to accept. PRs without prior discussion will most likely be immediately closed.

Changes are expected to be tested locally before a PR is submitted, to unnecessary reviews. For larger changes that need work over a longer time do mark these PRs as drafts. PRs need to fulfill the following requirements to be accepted.

* New code has tests where applicable.
* Linter does not report any errors.
* All unit and integration tests pass.

## Developing

The following tools are required to run the tests properly.

* go
* [golangci-lint](https://github.com/golangci/golangci-lint)
* [goreleaser](https://github.com/goreleaser/goreleaser)
* [docker](https://docs.docker.com/get-started/get-docker/)

Run the linter and the unit tests to quickly validate changes.

```shell
make lint test-unit
```

Run the e2e tests which take a bit more time. When run locally and in PRs only the latest versions of Containerd and Kubernetes will be tested. The nightly tests will run the tests with full coverage for all supported versions.

> [!NOTE]
> On macOS with Docker Desktop, you may need to point to the Docker socket manually if you see an error.
>
> ```shell
> export DOCKER_HOST=unix://${HOME}/.docker/run/docker.sock
> ```

```shell
make test-integration-containerd
make test-integration-kubernetes
```

### Helm Documentation

Changes to the Helm chart values will require the documentation to be regenerated.

```shell
make helm-docs
```

## Debugging

Run the Kubernetes integration tests, which create a Kind cluster and deploy Spegel into it.

```shell
make test-integration-kubernetes
```

To increase the timeout or tweak options, run the test directly from the directory:

```shell
cd test/integration/kubernetes
INTEGRATION_TEST_STRATEGY="fast" IMG_REF=<image-ref> go test -v -timeout 400s -count 1 ./...
```

After the command has run you can get a kubeconfig file to access the cluster and do any debugging. The Kind cluster name is created by the test; use `kind get clusters` to find it.

```shell
kind get clusters
kind get kubeconfig --name <cluster-name> > kubeconfig
export KUBECONFIG=$(pwd)/kubeconfig
kubectl -n spegel get pods
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
