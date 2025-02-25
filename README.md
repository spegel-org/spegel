# Spegel

Spegel, mirror in Swedish, is a stateless cluster local OCI registry mirror.

<p align="center">
  <img src="https://spegel.dev/images/overview.gif">
</p>

## Features

Spegel is for you if you are looking to do any of the following.

* Locally cache images from external registries with no explicit configuration.
* Avoid cluster failure during external registry downtime.
* Improve image pull speed and pod startup time by pulling images from the local cache first.
* Avoid rate-limiting when pulling images from external registries (e.g. Docker Hub).
* Decrease egressing traffic outside of the clusters network.
* Increase image pull efficiency in edge node deployments.

## Getting Started

Read the [getting started](https://spegel.dev/docs/getting-started/) guide to deploy Spegel.

## Contributing

Read [contribution guidelines](./CONTRIBUTING.md) for instructions on how to build and test Spegel.

## Community 
[Slack](https://communityinviter.com/apps/kubernetes/community) - Join us in the Kubernetes slack in
the #spegel channel

[Community Meeting](https://spegel.dev/project/community/#meeting) - Every Tuesday at 17:00 CET ask
questions on issues, roadmap, and meet the maintainers. 

## Acknowledgements

Spegel was initially developed at [Xenit AB](https://xenit.se/).

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
