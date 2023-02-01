# Spegel

Spegel, mirror in Swedish, is a stateless distributed mirror registry built of Kubernetes.

## Background

OCI registries are critical for the operation of a Kuberentes cluster. A lot of people may not think about registry up time or image pull speed. Others may just neglect it as doing something about it requires a lot of effort.
Spegel is a simple solution for a lot of the common issues fased with depending on public registries while requiring little operational effort. The experience with Kubernetes is great when a Pod is scheduled on a Node that has
already pulled the required images. It is already cached locally no dependencies on external resources. The question is why this experience can't be offered on all Nodes in a cluster. Spegel solves this problem by enabling
all Nodes in a Kubernetes cluster to share locally cached images with each other, as long as one Node in the cluster has an image stored locally all other Nodes will be able to fetch their copy from within the cluster, skipping
internet egressing traffic all together.

## Installation

Easiest method to install Spegel is with the [Helm Chart](./charts/spegel).

```shell
kubectl create namespace spegel
helm upgrade --install --version <version> spegel oci://ghcr.io/xenitab/helm-charts/spegel
```

## Architecture

In its core Spegel is a pull only OCI registry which runs locally on every Node in the Kubernetes cluster. Containerd is configured to use the local registry as a mirror, which would serve the image from within the cluster or from the source registry.

<p align="center">
  <img src="./assets/basic.jpg">
</p>

An image pull with spegel is completed with the following steps.

1. Containerd needs to pull an image.
2. Starts making requests for each component of the image.
3. Mirror configuration routes the requests to the local Spegel instance.
4. Spegel receives the request and will try to find another Node which already has the requested component.
5. If no Node is found 404 is returned, causing containerd to make the request to the original registry.
6. If a Node is found the request is forwarded to the Nodes Spegel instance.
7. Spegel will see the request is external and serve the image component as a image pull request.
8. All components are fetched completing the image pull.

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
