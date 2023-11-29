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

Spegel has been tested on the following Kubernetes flavors for compatibility. Some flavors may require additional steps while others will work right out of the box.

| Flavor | Status | Comment |
| --- | :---: | --- |
| AKS | :green_circle: | Verified to work out of the box. |
| EKS | :yellow_circle: | Discard unpacked layers is enabled by default and needs to be disabled. |
| GKE | :red_circle: | Not supported due to registry config path not being set. |
| Minikube | :green_circle: | Verified to work out of the box. |
| k3s | :yellow_circle: | See notes below |

## k3s

1. On each node run `cp /var/lib/rancher/k3s/agent/etc/containerd/config.toml /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl`
2. Add the following to `/var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl`

    ```toml
    [plugins."io.containerd.grpc.v1.cri".registry]
      config_path = "/var/lib/rancher/k3s/agent/etc/containerd/certs.d"
    ```

3. Restart k3s on all nodes

4. In the Spegel Helm chart set `.spegel.containerdSock` to `/run/k3s/containerd/containerd.sock` and `.spegel.containerdRegistryConfigPath` to `/var/lib/rancher/k3s/agent/etc/containerd/certs.d`

5. See [#212](https://github.com/XenitAB/spegel/issues/212) for more information
