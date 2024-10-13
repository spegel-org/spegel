# Spegel

Spegel, mirror in Swedish, is a stateless cluster local OCI registry mirror.

<p align="center">
  <img src="./assets/overview.gif">
</p>

## Use Cases

Spegel is for you if you are looking to do any of the following:

* Locally cache images from external registries with no explicit configuration.
* Avoid cluster failure during external registry downtime.
* Improve image pull speed and pod startup time by pulling images from the local cache first.
* Avoid rate-limiting when pulling images from external registries (e.g. Docker Hub).
* Decrease egressing traffic outside of the clusters network.
* Increase image pull efficiency in edge node deployments.

## Background

Kubernetes does a great job at distributing workloads on multiple nodes. Allowing node failures to occur without affecting uptime. A critical component for this to work is that each node has to be able to pull the workload images before they can start. Each replica running on a node will incur a pull operation. The images may be pulled from geographically close registries within the cloud provider, public registries, or self-hosted registries. This process has a flaw in that each node has to make this round trip separately. Why can't the nodes share the image among themselves?

Spegel enables each node in a Kubernetes cluster to act as a local registry mirror, allowing nodes to share images between themselves. Any image already pulled by a node will be available for any other node in the cluster to pull.

This has the benefit of reducing workload startup times and egress traffic as images will be stored locally within the cluster. On top of that it allows the scheduling of new workloads even when external registries are down.

## Installation

Before installing Spegel check the [compatibility guide](./docs/COMPATIBILITY.md) to make sure that it will work with your specific Kubernetes flavor. If everything checks out, the easiest method to deploy Spegel is with Helm.

```shell
helm upgrade --create-namespace --namespace spegel --install --version v0.0.27 spegel oci://ghcr.io/spegel-org/helm-charts/spegel
```

Refer to the [Helm Chart](./charts/spegel) for detailed configuration documentation.

## FAQ

Please consult the [FAQ](./docs/FAQ.md) if you run into any problems.

## Developing

See [contribution guidelines](./CONTRIBUTING.md) for instructions on how to build and test Spegel.

## Architecture

Spegel can run as a stateless application by exploiting the fact that an image pulled by a node is not immediately garbage collected. Spegel is deployed as a Daemonset on each node which acts as both the registry and mirror. Each instance is reachable both locally through a host port and a Service. This enables Containerd to be configured to use the localhost interface as a registry mirror and for Spegel instances to forward requests to each other.

<p align="center">
  <img src="./assets/architecture.jpg">
</p>

Images are composed of multiple layers which are stored as individual files on the node disk. Each layer has a digest which is its identifier. Every node advertises the digests which are stored locally on disk. Kademlia is used to enable a distributed advertisement and lookup of digests. An image pull consists of multiple HTTP requests with one request per digest. The request is first sent to Spegel when an image is pulled if it is configured to act as the mirror for the registry. Spegel will lookup the digest within the cluster to see if any node has advertised that they have it. If a node is found the request will be forwarded to that Spegel instance which will serve the file with the specified digest. If a node is not found a 404 response will be returned and Containerd will fallback to using the actual remote registry.

In its core Spegel is a pull only OCI registry which runs locally on every Node in the Kubernetes cluster. Containerd is configured to use the local registry as a mirror, which would serve the image from within the cluster or from the source registry.

## Alternatives

### Private Registry 

A common practice, especially for larger enterprises, is to run a private registry like Harbor to replicate images from public registries, storing them within the private network close to the cluster.
This is a great option for those who have the time and budget to invest in running and managing the infrastructure. For others, it may be a good practice but unattainable in reality.
Spegel does not aim to replace projects like [Harbor](https://github.com/goharbor/harbor) or [Zot](https://github.com/project-zot/zot) but instead complements them. Having a persistent copy of public images stored geographically close to a cluster is great. Spegel will however enable
nodes to pull from images closer as long as the images are somewhere within the cluster. Additionally, there is no guarantee that a self-managed private registry is always available. In these scenarios
running Spegel is like wearing both belt and suspenders.

### Dragonfly

[Dragonfly](https://github.com/dragonflyoss/Dragonfly2) is a great project that has been around for a while. In some aspects, Spegel takes inspiration from the work done by Dragonfly. 
The difference is that Spegel aims to solve a smaller problem set. While it may mean fewer features it also means fewer moving components. Dragonfly requires both Redis and MySQL which 
increases the resource consumption and burden on end users to manage additional resources. It also increases the risk of errors occurring during critical moments. The benefit of Spegel
is that it is stateless meaning that any temporary failure of nodes and communication should be easily resolved automatically.

### Kraken

[Kraken](https://github.com/uber/kraken) implements a similar solution to Spegel with its P2P agent component. It is however not heavily maintained, meaning that new features and security updates will not be added.
The problem set that Kraken is attempting to solve is however different from Spegel. It's focused on speeding up image distribution from registries serving thousands of large images. It does this by
having trackers and seeders distribute image layers through a BitTorrent-like method. This means that Kraken requires more moving components to function. Kraken also does not support using it
as a transparent pull-through mirror. Meaning that any image that is supposed to be pulled through Kraken will require changing the registry URL in the image name. This has to be done for all
Pods in the cluster. 

## Acknowledgements

Spegel was initially developed at [Xenit AB](https://xenit.se/).

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
