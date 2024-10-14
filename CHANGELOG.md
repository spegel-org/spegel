# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## Unreleased

### Added 

- [#373](https://github.com/spegel-org/spegel/pull/373) Implement support to disable registry filtering in Containerd.

### Changed

### Deprecated

### Removed

### Fixed

### Security

## v0.0.27

### Fixed

- [#603](https://github.com/spegel-org/spegel/pull/603) Fix append to backup always happening.
- [#604](https://github.com/spegel-org/spegel/pull/604) Create empty backup directory when mirror directory is empty.

## v0.0.26

### Removed

- [#596](https://github.com/spegel-org/spegel/pull/596) Remove throttling from blobs.

### Fixed

- [#601](https://github.com/spegel-org/spegel/pull/601) Fix Containerd host mirror ordering.

## v0.0.25

### Added 

- [#578](https://github.com/spegel-org/spegel/pull/578) Add possibility to override environment variable NODE_IP.

### Changed

- [#575](https://github.com/spegel-org/spegel/pull/575) Update to Go v1.23.2.

### Fixed

- [#581](https://github.com/spegel-org/spegel/pull/581) Skip status response verification for containerd v2

## v0.0.24

### Added 

- [#538](https://github.com/spegel-org/spegel/pull/538) Replace mock OCI client with in memory client.
- [#552](https://github.com/spegel-org/spegel/pull/552) Add support for VerticalPodAutoscaler in the Helm chart.
- [#556](https://github.com/spegel-org/spegel/pull/556) Add configuration for revisionHistoryLimit in the Helm Chart.
- [#573](https://github.com/spegel-org/spegel/pull/573) Use buffer pool for proxy copying data.

### Changed

- [#518](https://github.com/spegel-org/spegel/pull/518) Extend tests for image.
- [#519](https://github.com/spegel-org/spegel/pull/519) Extend tests for containerd.
- [#520](https://github.com/spegel-org/spegel/pull/520) Add tests for metrics.
- [#536](https://github.com/spegel-org/spegel/pull/536) Update Go version to 1.22.5.
- [#547](https://github.com/spegel-org/spegel/pull/547) Set blob content type to disable detection.
- [#553](https://github.com/spegel-org/spegel/pull/553) Re-use resources value for initContainer in the Helm Chart.

### Deprecated

### Removed

- [#517](https://github.com/spegel-org/spegel/pull/517) Remove deprecated CopyLayer function.

### Fixed

- [#535](https://github.com/spegel-org/spegel/pull/535) Fix Docker build casing checks.

### Security

## v0.0.23

### Added

- [#388](https://github.com/spegel-org/spegel/pull/388) Add support for deploying the Grafana dashboard with the Helm chart.

### Changed

- [#475](https://github.com/spegel-org/spegel/pull/475) Move resolving ref to digest to manifest handler.
- [#477](https://github.com/spegel-org/spegel/pull/477) Refactor distribution ref to simplify registry routing.
- [#479](https://github.com/spegel-org/spegel/pull/479) Enable goimports linter and fix errors.
- [#480](https://github.com/spegel-org/spegel/pull/480) Enable ireturn linter and fix errors.
- [#481](https://github.com/spegel-org/spegel/pull/481) Enable perfsprint linter and fix errors.
- [#482](https://github.com/spegel-org/spegel/pull/482) Enable gocritic linter and fix errors.
- [#483](https://github.com/spegel-org/spegel/pull/483) Update errcheck linter configuration and fix errors.
- [#487](https://github.com/spegel-org/spegel/pull/487) Move mirror metrics code to mirror handler.
- [#488](https://github.com/spegel-org/spegel/pull/488) Update existing registry errors and add more detail.
- [#495](https://github.com/spegel-org/spegel/pull/495) Modify e2e tests to allow reusing the same kind cluster.
- [#498](https://github.com/spegel-org/spegel/pull/498) Update to Go 1.22.
- [#499](https://github.com/spegel-org/spegel/pull/499) Add paralleltest linter and set all unit tests to run in parallel.
- [#501](https://github.com/spegel-org/spegel/pull/501) Rename mock router to memory router and add tests.
- [#507](https://github.com/spegel-org/spegel/pull/507) Change default resolve timeout to 20ms.

### Fixed

- [#460](https://github.com/spegel-org/spegel/pull/460) Fix environment variable for http-bootstrap-addr flag.
- [#471](https://github.com/spegel-org/spegel/pull/471) Fix handler key in request logging.
- [#490](https://github.com/spegel-org/spegel/pull/490) Close immediate channel after writing to it to close wait group in merge logic.
- [#491](https://github.com/spegel-org/spegel/pull/491) Fix so that resolve timeout does not cancel mirroring attempts.
- [#496](https://github.com/spegel-org/spegel/pull/496) Fix p2p bootstrap to run on failed readiness check.

## v0.0.22

### Added 

- [#435](https://github.com/spegel-org/spegel/pull/435) Add pprof endpoints to enable profiling.
- [#434](https://github.com/spegel-org/spegel/pull/434) Add optional Containerd local content store to increase serve performance.
- [#438](https://github.com/spegel-org/spegel/pull/438) Set host path type for Containerd socket.
- [#449](https://github.com/spegel-org/spegel/pull/449) Replace zapr with slog and add log level configuration.

### Changed

- [#439](https://github.com/spegel-org/spegel/pull/439) Update Go version and fix toolchain version.

### Fixed

- [#452](https://github.com/spegel-org/spegel/pull/452) Fix Containerd Subscribe returning on any error.

### Security

- [#451](https://github.com/spegel-org/spegel/pull/451) Bump golang.org/x/net from 0.21.0 to 0.23.0.

## v0.0.21

### Added 

- [#421](https://github.com/spegel-org/spegel/pull/421) Add conformance tests to e2e test.
- [#424](https://github.com/spegel-org/spegel/pull/424) Add option to append mirror configuration instead of overwriting.
- [#429](https://github.com/spegel-org/spegel/pull/429) Add metrics to measure duration to resolve peers.

### Changed

- [#395](https://github.com/spegel-org/spegel/pull/395) Replace Gin with standard lib HTTP handler.
- [#430](https://github.com/spegel-org/spegel/pull/430) Replace XenitAB Kubernetes pkg with internal package.

### Fixed

- [#431](https://github.com/spegel-org/spegel/pull/431) Fix import error caused by invalid file name.

## v0.0.20
 
### Added 

- [#416](https://github.com/spegel-org/spegel/pull/416) Add image and Helm chart signing with Cosign.

### Changed

- [#411](https://github.com/spegel-org/spegel/pull/411) Replace XenitAB pkg with internal package.

### Fixed

- [#412](https://github.com/spegel-org/spegel/pull/412) Fix http bootstrapper arg annotation.

### Security

- [#409](https://github.com/spegel-org/spegel/pull/409) Bump protobuf to fix CVE-2024-24786.
- [#415](https://github.com/spegel-org/spegel/pull/415) Bump Libp2p to fix CVE-2024-22189.

## v0.0.19

> [!IMPORTANT]  
> The Spegel repository has been moved from XenitAB to a new GitHub organization.
> Make sure to update the organization in the image and chart references.

### Added 

- [#335](https://github.com/spegel-org/spegel/pull/335) Add k3s to compatibility guide.
- [#359](https://github.com/spegel-org/spegel/pull/359) Extend OCI client tests.
- [#365](https://github.com/spegel-org/spegel/pull/365) Add support for throttling blob write speed.
- [#386](https://github.com/spegel-org/spegel/pull/386) Add contributing guide.
- [#391](https://github.com/spegel-org/spegel/pull/391) Add documentation for EKS specific Containerd configuration.
- [#393](https://github.com/spegel-org/spegel/pull/393) Add environment variable configuration support.
- [#394](https://github.com/spegel-org/spegel/pull/394) Add `cgr.dev` to default registry mirrors in the Helm chart.
- [#398](https://github.com/spegel-org/spegel/pull/398) Document DigitalOcean incompatibility.

### Changed

- [#355](https://github.com/spegel-org/spegel/pull/355) Rename OCI function names.
- [#356](https://github.com/spegel-org/spegel/pull/356) Refactor OCI client test to simplify testing multiple implementations.
- [#357](https://github.com/spegel-org/spegel/pull/357) Replace mock Containerd store with real upstream store.
- [#367](https://github.com/spegel-org/spegel/pull/367) Update Go image to 1.21.7.
- [#376](https://github.com/spegel-org/spegel/pull/376) Change go directive to 1.21.
- [#383](https://github.com/spegel-org/spegel/pull/383) Bump libp2p to v0.33.0, replace deprecated Pretty function
- [#397](https://github.com/spegel-org/spegel/pull/397) Replace CopyLayer with GetBlob.
- [#400](https://github.com/spegel-org/spegel/pull/400) Update org imports from xenitab to spegel-org.
- [#402](https://github.com/spegel-org/spegel/pull/402) Update tests to use spegel-org instead of xenitab.

### Deprecated

### Removed

- [#371](https://github.com/spegel-org/spegel/pull/371) Remove state track retry on error.

### Fixed

- [#396](https://github.com/spegel-org/spegel/pull/396) Fix missing metrics when registering.
- [#408](https://github.com/spegel-org/spegel/pull/408) Fix int overflow for ARM builds.

### Security

## v0.0.18

### Added 

- [#331](https://github.com/spegel-org/spegel/pull/331) Document possible modifications required for k8s-digester.
- [#337](https://github.com/spegel-org/spegel/pull/337) Add HTTP bootstrapper.
- [#340](https://github.com/spegel-org/spegel/pull/340) Add Talos to compatibility.
- [#343](https://github.com/spegel-org/spegel/pull/343) Implement image event and add support for delete events.
- [#344](https://github.com/spegel-org/spegel/pull/344) Add support for multi arch images.
- [#347](https://github.com/spegel-org/spegel/pull/347) Add support for a custom http transport when proxying requests.
- [#352](https://github.com/spegel-org/spegel/pull/352) Add lip2p options to router.

### Changed

- [#319](https://github.com/spegel-org/spegel/pull/319) Move metrics definitions to separate package.
- [#322](https://github.com/spegel-org/spegel/pull/322) Refactor type of router resolve.
- [#325](https://github.com/spegel-org/spegel/pull/325) Refactor bootstrap to exit on error.
- [#326](https://github.com/spegel-org/spegel/pull/326) Clean up routing interface.
- [#328](https://github.com/spegel-org/spegel/pull/328) Move remaining packages to pkg.
- [#342](https://github.com/spegel-org/spegel/pull/342) Defer Containerd client creation until first accessed.
- [#348](https://github.com/spegel-org/spegel/pull/348) Change registry configuration to options.
- [#349](https://github.com/spegel-org/spegel/pull/349) Adjust router peer channel buffer size.

### Removed

- [#327](https://github.com/spegel-org/spegel/pull/327) Remove ConfigMap from RBAC.

## v0.0.17

### Added 

- [#299](https://github.com/spegel-org/spegel/pull/299) Add update strategy configuration to Helm chart.

### Changed

- [#291](https://github.com/spegel-org/spegel/pull/291) Move OCI package to pkg.
- [#306](https://github.com/spegel-org/spegel/pull/306) Realign the structs in attempt to minimise memory usage.

### Fixed

- [#309](https://github.com/spegel-org/spegel/pull/309) Fix label selectors on service monitors and metrics service.
- [#279](https://github.com/spegel-org/spegel/pull/279) Fix broken default value for additional mirror registries.
- [#284](https://github.com/spegel-org/spegel/pull/284) Fix Spegel support for ipv6.

## v0.0.16

### Fixed

- [#276](https://github.com/spegel-org/spegel/pull/276) Fix Golang image digest to use manifest list instead of AMD64.

## v0.0.15

### Added 

- [#270](https://github.com/spegel-org/spegel/pull/270) Add tests for local and external service port.
- [#262](https://github.com/spegel-org/spegel/pull/262) Enable misspell linter and fix spelling mistakes.
- [#263](https://github.com/spegel-org/spegel/pull/263) Enable testifylint linter and fix errors.
- [#269](https://github.com/spegel-org/spegel/pull/269) Set Go image version with digest in Dockerfile.

### Changed

- [#253](https://github.com/spegel-org/spegel/pull/253) Set klog logger to standardize output format.

### Fixed

- [#271](https://github.com/spegel-org/spegel/pull/271) Fix Spegel running on IPVS cluster.

## v0.0.14

### Added 

- [#237](https://github.com/spegel-org/spegel/pull/237) Verify discard unpacked layers setting.

### Fixed

- [#241](https://github.com/spegel-org/spegel/pull/241) Fix missing return on resolve error.
- [#223](https://github.com/spegel-org/spegel/pull/223) Propagate closing channel before resolve timeout.

### Security

- [#249](https://github.com/spegel-org/spegel/pull/249) Bump google.golang.org/grpc from 1.55.0 to 1.56.3

## v0.0.13

### Added 

- [#195](https://github.com/spegel-org/spegel/pull/195) Fix daemonset argument namespace to use helper-defined namespace value.

### Changed

- [#164](https://github.com/spegel-org/spegel/pull/164) Update Go to 1.21.
- [#211](https://github.com/spegel-org/spegel/pull/211) Replace factory with adress filter to remove loopback addresses.
- [#219](https://github.com/spegel-org/spegel/pull/219) Use release name instead of namespace for name of leader election configmap.
- [#215](https://github.com/spegel-org/spegel/pull/215) Support for servicemonitor labels, interval and timeout in helm chart.

### Fixed

- [#233](https://github.com/spegel-org/spegel/pull/233) Fix address filtering to remove localhost from host.

### Security

- [#235](https://github.com/spegel-org/spegel/pull/235) Bump golang.org/x/net from 0.14.0 to 0.17.0.

## v0.0.12

### Added 

- [#182](https://github.com/spegel-org/spegel/pull/182) Add lscr.io as default registry.

### Fixed

- [#181](https://github.com/spegel-org/spegel/pull/181) Fix mirroring images using index files without a media type.
- [#191](https://github.com/spegel-org/spegel/pull/191) Fix Containerd config path verification.

### Security

- [#184](https://github.com/spegel-org/spegel/pull/184) Bump github.com/libp2p/go-libp2p from 0.27.7 to 0.30.0.

## v0.0.11

### Added 

- [#170](https://github.com/spegel-org/spegel/pull/170) Backup existing Containerd mirror configuration.
- [#171](https://github.com/spegel-org/spegel/pull/171) Add option to disable resolve.

### Changed

- [#174](https://github.com/spegel-org/spegel/pull/174) Modify error handling in state tracking to avoid exiting.

## v0.0.10

### Added 

- [#145](https://github.com/spegel-org/spegel/pull/145) Add new field to override Helm chart namespace.
- [#153](https://github.com/spegel-org/spegel/pull/153) Add option to disable resolving latest tags.
- [#156](https://github.com/spegel-org/spegel/pull/156) Add validation of mirror configuration on start.

### Changed

- [#151](https://github.com/spegel-org/spegel/pull/151) Refactor containerd mirror tests and remove utils package.

### Removed

- [#160](https://github.com/spegel-org/spegel/pull/160) Remove X-Spegel-Registry header.
- [#161](https://github.com/spegel-org/spegel/pull/161) Remove X-Spegel-Mirror header.
- [#162](https://github.com/spegel-org/spegel/pull/162) Remove X-Spegel-External header.

### Fixed

- [#152](https://github.com/spegel-org/spegel/pull/152) Fix image parsing to allow only passing digest through image reference.
- [#158](https://github.com/spegel-org/spegel/pull/158) Fix Containerd verify with check for empty configuration path.
- [#163](https://github.com/spegel-org/spegel/pull/163) Remove unneeded namespace in role binding.

## v0.0.9

### Changed

- [#138](https://github.com/spegel-org/spegel/pull/138) Set image digest in Helm chart.

### Fixed

- [#141](https://github.com/spegel-org/spegel/pull/141) Fix platform matching and add tests for getting image digests.

## v0.0.8

### Added 

- [#125](https://github.com/spegel-org/spegel/pull/125) Add retry mirroring to new peer if current peer fails.
- [#127](https://github.com/spegel-org/spegel/pull/127) Add configuration for resolve retry and timeout.

### Changed

- [#107](https://github.com/spegel-org/spegel/pull/107) Refactor image references with generic implementation.
- [#114](https://github.com/spegel-org/spegel/pull/114) Move mirror configuration to specific OCI implementation.
- [#117](https://github.com/spegel-org/spegel/pull/117) Update Containerd client to 1.7.
- [#126](https://github.com/spegel-org/spegel/pull/126) Refactor registry implementation to not require separate handler.
- [#132](https://github.com/spegel-org/spegel/pull/132) Extend tests to validate single node and mirror fallback.
- [#133](https://github.com/spegel-org/spegel/pull/133) Use routing table size for readiness check.

### Removed

- [#113](https://github.com/spegel-org/spegel/pull/113) Remove image filter configuration.

## v0.0.7

### Changed

- [#82](https://github.com/spegel-org/spegel/pull/82) Filter out localhost from advertised IPs.
- [#89](https://github.com/spegel-org/spegel/pull/89) Remove p2p route table check on startup.
- [#91](https://github.com/spegel-org/spegel/pull/91) Adjust tolerations and node selector.

## v0.0.6

### Changed

- [#42](https://github.com/spegel-org/spegel/pull/42) Only use bootstrap function for initial peer discovery.
- [#66](https://github.com/spegel-org/spegel/pull/66) Move mirror configuration logic to run as an init container.
 
### Fixed

- [#71](https://github.com/spegel-org/spegel/pull/71) Fix priority class name.

## v0.0.5

### Added 

- [#29](https://github.com/spegel-org/spegel/pull/29) Make priority class name configurable and set a default value.
- [#49](https://github.com/spegel-org/spegel/pull/49) Add registry.k8s.io to registry mirror list.
- [#56](https://github.com/spegel-org/spegel/pull/56) Add gcr.io and k8s.gcr.io registries to default list.

### Changed
 
- [#32](https://github.com/spegel-org/spegel/pull/32) Update Go to 1.20.
- [#33](https://github.com/spegel-org/spegel/pull/33) Remove containerd info call when handling manifest request.
- [#48](https://github.com/spegel-org/spegel/pull/48) Replace multierr with stdlib errors join.
- [#54](https://github.com/spegel-org/spegel/pull/54) Refactor metrics and add documentation.

### Fixed

- [#51](https://github.com/spegel-org/spegel/pull/51) Filter tracked images to only included mirrored registries.
- [#52](https://github.com/spegel-org/spegel/pull/52) Return error when image reference is not valid.
- [#55](https://github.com/spegel-org/spegel/pull/55) Fix filters by merging them into a single statement.
- [#53](https://github.com/spegel-org/spegel/pull/53) Include error from defer in returned error.

## v0.0.4

### Fixed

- [#26](https://github.com/spegel-org/spegel/pull/26) Replace topology keys with optional topology aware hints.

## v0.0.3

### Added 

- [#18](https://github.com/spegel-org/spegel/pull/18) Add support to use Spegel instance on another node.

### Changed

- [#21](https://github.com/spegel-org/spegel/pull/21) Allow external mirror request to resolve to mirror instance.
