> [!NOTE]  
> We’ve started hosting community meetings every Tuesday at 17:00 CET. Find out how to participate at https://spegel.dev/project/community/#meeting.

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
* Optional OpenTelemetry tracing – see the [OTEL quick start](./docs/otel/README.md).

## Getting Started

Read the [getting started](https://spegel.dev/docs/getting-started/) guide to deploy Spegel.

## Contributing

Read [contribution guidelines](./CONTRIBUTING.md) for instructions on how to build and test Spegel.

## Acknowledgements

Spegel was initially developed at [Xenit AB](https://xenit.se/).

## License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.
