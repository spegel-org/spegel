# Compatibility 

Currently Spegel only works with Containerd, in future other container runtime interfaces may be supported. Spegel relies on [Containerd registry mirroring](https://github.com/containerd/containerd/blob/main/docs/hosts.md#cri) to route requests to the correct destination.
This requires Containerd to be properly configured with a registry config path, without this Spegel will not be able to start. Some Kubernetes flavors come with this setting out of the box, while others do not. Spegel is not able to write this configuration for you as it requires a restart of Containerd to take effect.

```toml
version = 2

[plugins."io.containerd.grpc.v1.cri".registry]
   config_path = "/etc/containerd/certs.d"
```

Below is a list of Kubernetes flavors that Spegel has been tested on. Some flavors may require additional steps while other will work right out of the box.

## AKS

AKS will work out of the box as it comes with the required registry config path set out of the box.

## EKS

EKS will work out of the box as it comes with the required registry config path set out of the box.

## GKE

GKE will currently not work with Spegel. This is due to GKE using the old registry configuration format, that lacks certain settings Spegel relies on.
