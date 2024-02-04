# Compatibility 

Currently, Spegel only works with Containerd, in the future other container runtime interfaces may be supported. Spegel relies on [Containerd registry mirroring](https://github.com/containerd/containerd/blob/main/docs/hosts.md#cri) to route requests to the correct destination.
This requires Containerd to be properly configured, if it is not Spegel will exit. First of all the registry config path needs to be set, this is not done by default in Containerd. Second of all discarding unpacked layers cannot be enabled.
Some Kubernetes flavors come with this setting out of the box, while others do not. Spegel is not able to write this configuration for you as it requires a restart of Containerd to take effect.

```toml
version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
   config_path = "/etc/containerd/certs.d"
[plugins."io.containerd.grpc.v1.cri".containerd]
   discard_unpacked_layers = false
```

# Kubernetes

Spegel has been tested on the following Kubernetes distributions for compatibility. Green status means Spegel will work out of the box, yellow will require additional configuration, and red means that Spegel will not work.

| Status | Distribution |
| --- | :---: | --- |
| :green_circle: | AKS |
| :green_circle: | Minikube |
| :yellow_circle: | EKS |
| :yellow_circle: | Talos |
| :red_circle: | GKE |

## EKS

Discard unpacked layers is enabled by default and needs to be disabled.

## Talos

Talos comes with Pod Security Admission [pre-configured](https://www.talos.dev/latest/kubernetes-guides/configuration/pod-security/). The default profile is too restrictive and needs to be changed to privileged.

```shell
kubectl label namespace spegel pod-security.kubernetes.io/enforce=privileged
```

Talos also uses a different path as its Containerd registry config path.

```yaml
spegel:
  containerdRegistryConfigPath: /etc/cri/conf.d/hosts
```

# GKE

GKE does not set the registry config path in its Containerd configuration. On top of that it uses the old mirror configuration for the internal mirroring service.