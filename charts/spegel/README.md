# Spegel

Stateless cluster local OCI registry mirror.

Read the [getting started](https://spegel.dev/docs/getting-started/) guide to deploy Spegel.

## Values

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| affinity | object | `{}` | Affinity settings for pod assignment. |
| basicAuthSecretName | string | `""` | Name of secret containing basic authentication credentials for registry. |
| clusterDomain | string | `"cluster.local."` | Domain configured for service domain names. |
| commonLabels | object | `{}` | Common labels to apply to all rendered resources. |
| daemonsetAnnotations | object | `{}` | Annotations to add to the DaemonSet. |
| fullnameOverride | string | `""` | Overrides the full name of the chart. |
| grafanaDashboard.annotations | object | `{}` | Annotations to add to the Sidecar configMap or GrafanaDashboard. |
| grafanaDashboard.enabled | bool | `false` | If true creates a Grafana dashboard. |
| grafanaDashboard.grafanaOperator.allowCrossNamespaceImport | bool | `true` | If true allows for a Grafana in any namespace to access this GrafanaDashboard. |
| grafanaDashboard.grafanaOperator.folder | string | `""` | Folder to create the dashboard in. |
| grafanaDashboard.grafanaOperator.matchLabels | object | `{}` | Selected labels for Grafana instance. |
| grafanaDashboard.grafanaOperator.resyncPeriod | string | `"10m"` | Resync period for the Grafana operator to check for updates to the dashboard. |
| grafanaDashboard.labels | object | `{}` | Labels to add to the Sidecar configMap or GrafanaDashboard. |
| grafanaDashboard.mode | string | `"Sidecar"` | Mode for Grafana dashboard creation. Valid values are `Sidecar` and `GrafanaOperator`. |
| image.digest | string | `""` | Image digest. |
| image.pullPolicy | string | `"IfNotPresent"` | Image Pull Policy. |
| image.repository | string | `"ghcr.io/spegel-org/spegel"` | Image repository. |
| image.tag | string | `""` | Overrides the image tag whose default is the chart appVersion. |
| imagePullSecrets | list | `[]` | Image Pull Secrets |
| livenessProbe.enabled | bool | `false` | When enabled a liveness probe will be added to the registry. |
| nameOverride | string | `""` | Overrides the name of the chart. |
| namespaceOverride | string | `""` | Overrides the namespace where spegel resources are installed. |
| nodeSelector | object | `{"kubernetes.io/os":"linux"}` | Node selector for pod assignment. |
| podAnnotations | object | `{}` | Annotations to add to the pod. |
| podSecurityContext | object | `{}` | Security context for the pod. |
| priorityClassName | string | `"system-node-critical"` | Priority class name to use for the pod. |
| resources | object | `{"limits":{"memory":"128Mi"},"requests":{"memory":"128Mi"}}` | Resource requests and limits for the Spegel container. |
| revisionHistoryLimit | int | `10` | The number of old history to retain to allow rollback. |
| securityContext | object | `{"readOnlyRootFilesystem":true}` | Security context for the Spegel container. |
| service.bootstrap.annotations | object | `{}` | Annotations to add to the bootstrap service |
| service.cleanup.annotations | object | `{}` | Annotations to add to the cleanup service (used in post-delete hook) |
| service.cleanup.port | int | `8080` | Port to expose cleanup probe on. |
| service.metrics.annotations | object | `{}` | Annotations to add to the metrics service |
| service.metrics.port | int | `9090` | Port to expose the metrics via the service. |
| service.registry.annotations | object | `{}` | Annotations to add to the registry service |
| service.registry.hostPort | int | `30020` | Local host port to expose the registry. |
| service.registry.nodeIp | string | `""` | Override the NODE_ID environment variable. It defaults to the field status.hostIP |
| service.registry.nodePort | int | `30021` | Node port to expose the registry via the service. |
| service.registry.port | int | `5000` | Port to expose the registry via the service. |
| service.registry.topologyAwareHintsEnabled | bool | `true` | If true adds topology aware hints annotation to node port service. |
| service.registry.usePreferSameNodeTrafficDistribution | bool | `false` | Use PreferSameNode traffic distribution on the node port service instead of using an additional mirror registry on a container host port. |
| service.router.port | int | `5001` | Port to expose the router via the service. |
| serviceAccount.annotations | object | `{}` | Annotations to add to the service account |
| serviceAccount.name | string | `""` | The name of the service account to use. If not set and create is true, a name is generated using the fullname template. |
| serviceMonitor.enabled | bool | `false` | If true creates a Prometheus Service Monitor. |
| serviceMonitor.interval | string | `"60s"` | Prometheus scrape interval. |
| serviceMonitor.labels | object | `{}` | Service monitor specific labels for prometheus to discover servicemonitor. |
| serviceMonitor.metricRelabelings | list | `[]` | List of relabeling rules to apply to the samples before ingestion. |
| serviceMonitor.relabelings | list | `[]` | List of relabeling rules to apply the target’s metadata labels. |
| serviceMonitor.scrapeTimeout | string | `"30s"` | Prometheus scrape interval timeout. |
| spegel.additionalMirrorTargets | list | `[]` | Additional target mirror registries other than Spegel. |
| spegel.containerdContentPath | string | `"/var/lib/containerd/io.containerd.content.v1.content"` | Path to Containerd content store.. |
| spegel.containerdMirrorAdd | bool | `true` | If true Spegel will add mirror configuration to the node. |
| spegel.containerdNamespace | string | `"k8s.io"` | Containerd namespace where images are stored. |
| spegel.containerdRegistryConfigPath | string | `"/etc/containerd/certs.d"` | Path to Containerd mirror configuration. |
| spegel.containerdSock | string | `"/run/containerd/containerd.sock"` | Path to Containerd socket. |
| spegel.dataDir.enabled | bool | `true` | Enable persistent storage for the P2P private key. Uses a hostPath volume to store the key on the node. The key file is created with mode 0600. |
| spegel.dataDir.hostPath | string | `"/var/lib/spegel"` | Host path mounted into the pod for persistence. |
| spegel.dataDir.path | string | `"/var/lib/spegel"` | Path in the container where data is stored. |
| spegel.debugWebEnabled | bool | `true` | When true enables debug web page. |
| spegel.logLevel | string | `"INFO"` | Minimum log level to output. Value should be DEBUG, INFO, WARN, or ERROR. |
| spegel.mirrorResolveRetries | int | `3` | Max amount of mirrors to attempt. |
| spegel.mirrorResolveTimeout | string | `"20ms"` | Max duration spent finding a mirror. |
| spegel.mirroredRegistries | list | `[]` | Registries for which mirror configuration will be created. Empty means all registires will be mirrored. |
| spegel.prependExisting | bool | `false` | When true existing mirror configuration will be kept and Spegel will prepend it's configuration. |
| spegel.registryFilters | list | `[]` | Regular expressions to filter out tags/registries. If empty, all registries/tags are resolved. |
| spegel.resolveTags | bool | `true` | When true Spegel will resolve tags to digests. |
| tolerations | list | `[{"key":"CriticalAddonsOnly","operator":"Exists"},{"effect":"NoExecute","operator":"Exists"},{"effect":"NoSchedule","operator":"Exists"}]` | Tolerations for pod assignment. |
| updateStrategy | object | `{}` | An update strategy to replace existing pods with new pods. |
| verticalPodAutoscaler.controlledResources | list | `[]` | List of resources that the vertical pod autoscaler can control. Defaults to cpu and memory |
| verticalPodAutoscaler.controlledValues | string | `"RequestsAndLimits"` | Specifies which resource values should be controlled: RequestsOnly or RequestsAndLimits. |
| verticalPodAutoscaler.enabled | bool | `false` | If true creates a Vertical Pod Autoscaler. |
| verticalPodAutoscaler.maxAllowed | object | `{}` | Define the max allowed resources for the pod |
| verticalPodAutoscaler.minAllowed | object | `{}` | Define the min allowed resources for the pod |
| verticalPodAutoscaler.recommenders | list | `[]` | Recommender responsible for generating recommendation for the object. List should be empty (then the default recommender will generate the recommendation) or contain exactly one recommender. |
| verticalPodAutoscaler.updatePolicy.minReplicas | int | `2` | Specifies minimal number of replicas which need to be alive for VPA Updater to attempt pod eviction |
| verticalPodAutoscaler.updatePolicy.updateMode | string | `"Auto"` | Specifies whether recommended updates are applied when a Pod is started and whether recommended updates are applied during the life of a Pod. Possible values are "Off", "Initial", "Recreate", and "Auto". |
