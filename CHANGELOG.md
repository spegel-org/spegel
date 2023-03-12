# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added 

- [#29](https://github.com/XenitAB/spegel/pull/29) Make priority class name configurable and set a default value.
- [#49](https://github.com/XenitAB/spegel/pull/49) Add registry.k8s.io to registry mirror list.

### Changed
 
- [#32](https://github.com/XenitAB/spegel/pull/32) Update Go to 1.20.
- [#33](https://github.com/XenitAB/spegel/pull/33) Remove containerd info call when handling manifest request.
- [#48](https://github.com/XenitAB/spegel/pull/48) Replace multierr with stdlib errors join.
- [#54](https://github.com/XenitAB/spegel/pull/54) Refactor metrics and add documentation.

### Deprecated

### Removed

### Fixed

- [#51](https://github.com/XenitAB/spegel/pull/51) Filter tracked images to only included mirrored registries.
- [#52](https://github.com/XenitAB/spegel/pull/52) Return error when image reference is not valid.
- [#55](https://github.com/XenitAB/spegel/pull/55) Fix filters by merging them into a single statement.
- [#53](https://github.com/XenitAB/spegel/pull/53) Include error from defer in returned error.

### Security

## v0.0.4

### Fixed

- [#26](https://github.com/XenitAB/spegel/pull/26) Replace topology keys with optional topology aware hints.

## v0.0.3

### Added 

- [#18](https://github.com/XenitAB/spegel/pull/18) Add support to use Spegel instance on another node.

### Changed

- [#21](https://github.com/XenitAB/spegel/pull/21) Allow external mirror request to resolve to mirror instance.
