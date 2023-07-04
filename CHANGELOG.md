# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added 

- [#125](https://github.com/XenitAB/spegel/pull/125) Add retry mirroring to new peer if current peer fails.
- [#127](https://github.com/XenitAB/spegel/pull/127) Add configuration for resolve retry and timeout.

### Changed

- [#107](https://github.com/XenitAB/spegel/pull/107) Refactor image references with generic implementation.
- [#114](https://github.com/XenitAB/spegel/pull/114) Move mirror configuration to specific OCI implementation.
- [#117](https://github.com/XenitAB/spegel/pull/117) Update Containerd client to 1.7.
- [#126](https://github.com/XenitAB/spegel/pull/126) Refactor registry implementation to not require separate handler.
- [#132](https://github.com/XenitAB/spegel/pull/132) Extend tests to validate single node and mirror fallback.

### Deprecated

### Removed

- [#113](https://github.com/XenitAB/spegel/pull/113) Remove image filter configuration.

### Fixed

### Security

## v0.0.7

### Changed

- [#82](https://github.com/XenitAB/spegel/pull/82) Filter out localhost from advertised IPs.
- [#89](https://github.com/XenitAB/spegel/pull/89) Remove p2p route table check on startup.
- [#91](https://github.com/XenitAB/spegel/pull/91) Adjust tolerations and node selector.

## v0.0.6

### Changed

- [#42](https://github.com/XenitAB/spegel/pull/42) Only use bootstrap function for initial peer discovery.
- [#66](https://github.com/XenitAB/spegel/pull/66) Move mirror configuration logic to run as an init container.
 
### Fixed

- [#71](https://github.com/XenitAB/spegel/pull/71) Fix priority class name.

## v0.0.5

### Added 

- [#29](https://github.com/XenitAB/spegel/pull/29) Make priority class name configurable and set a default value.
- [#49](https://github.com/XenitAB/spegel/pull/49) Add registry.k8s.io to registry mirror list.
- [#56](https://github.com/XenitAB/spegel/pull/56) Add gcr.io and k8s.gcr.io registries to default list.

### Changed
 
- [#32](https://github.com/XenitAB/spegel/pull/32) Update Go to 1.20.
- [#33](https://github.com/XenitAB/spegel/pull/33) Remove containerd info call when handling manifest request.
- [#48](https://github.com/XenitAB/spegel/pull/48) Replace multierr with stdlib errors join.
- [#54](https://github.com/XenitAB/spegel/pull/54) Refactor metrics and add documentation.

### Fixed

- [#51](https://github.com/XenitAB/spegel/pull/51) Filter tracked images to only included mirrored registries.
- [#52](https://github.com/XenitAB/spegel/pull/52) Return error when image reference is not valid.
- [#55](https://github.com/XenitAB/spegel/pull/55) Fix filters by merging them into a single statement.
- [#53](https://github.com/XenitAB/spegel/pull/53) Include error from defer in returned error.

## v0.0.4

### Fixed

- [#26](https://github.com/XenitAB/spegel/pull/26) Replace topology keys with optional topology aware hints.

## v0.0.3

### Added 

- [#18](https://github.com/XenitAB/spegel/pull/18) Add support to use Spegel instance on another node.

### Changed

- [#21](https://github.com/XenitAB/spegel/pull/21) Allow external mirror request to resolve to mirror instance.
