# FAQ

Frequently asked questions, please read these before creating a new issue.

## Can I use Spegel in production?

We have been running Spegel in multiple production Kubernetes clusters at Xenit since the first release without any issues. The great thing is that pulling images would not stop working if you for some reason would find an issue with Spegel.
A fallback to the original registry will always occur if Spegel can't be reached or serve the requested image.

## How do I know that Spegel is working? 

Spegel is meant to be a painless experience to install, meaning that it may be difficult initially to know if things are working or not. Simply put a good indicator that things are working is if all Spegel pods have started and are in a ready state.
Spegel does a couple of checks on startup to verify that any required configuration is correct, if it is not it will exit with an error. While it runs it will log all received requests, both those it mirrors and it serves.

An incoming request to Spegel that is mirrored will receive the following log.

```
{"level":"info","ts":1692304805.9038486,"caller":"gin@v0.0.9/logger.go:53","msg":"","path":"/v2/library/nginx/blobs/sha256:1cb127bd932119089b5ffb612ffa84537ddd1318e6784f2fce80916bbb8bd166","status":200,"method":"GET","latency":0.005075836,"ip":"172.18.0.5","handler":"mirror"}
```

While the Spegel instance on the other end will log.

```
{"level":"info","ts":1692304805.9035861,"caller":"gin@v0.0.9/logger.go:53","msg":"","path":"/v2/library/nginx/blobs/sha256:1cb127bd932119089b5ffb612ffa84537ddd1318e6784f2fce80916bbb8bd166","status":200,"method":"GET","latency":0.003644997,"ip":"172.18.0.5","handler":"blob"}
```

## Why am I not able to pull the new version of my tagged image?

Reusing the same tag multiple times for different versions of an image is generally a bad idea. The most common scenario is the use of the `latest` tag. This makes it difficult to determine which version of the image is being used. On top of that, the image will not be updated if it is already cached on the node.
Some people have chosen to power forward with reusing tags and chosen to instead set the image pull policy to `AlwaysPull`, forcing the image to be updated every time a pod is started. This will however not work with Spegel as the tag could be resolved by another node in the cluster resulting in the same "old" image being pulled.
There are two solutions to work around this problem, allowing users to continue with their way of working before using Spegel.

The best and preferable solution is to deploy [k8s-digester](https://github.com/google/k8s-digester) alongside Spegel. This will allow you to enjoy all the benefits of Spegel will continuously updating image tag versions. The way it works is that k8s-digester will, for each pod created, resolve tags to image digests and add them to the image reference.
This means that all pods that originally reference images by tag will instead do so with digest. This means that k8s-digester will resolve the new digest for a tag if a new version is pushed to the registry. Using k8s-digester means that tags will be updated while using Spegel to distribute the layers between nodes. It also means that Spegel will be able
to continue distributing images if the external registry became unavailable. The reason this works is that the mutating webhook is configured to ignore errors, and instead, Spegel will be used to resolve the tag to a digest.

The second option, which should be used only if using k8s-digester is not possible, is to disable tag resolving altogether in Spegel. There are two options when doing this. It can either be disabled only for `latest` tags or for all tags. 

This can be done by changing the Helm charts values from their defaults.

```yaml
spegel:
  resolveTags: false
  resolveLatestTag: false
```

Please note that this does however remove Spegel's ability to protect against registry outages for any images referenced by tags.

## Why am I able to pull private images without image pull secrets?

An image pulled by a Kubernetes node is cached locally on disk. Meaning that other pods running on the same node that require the same image do not have to pull the same image again. Spegel relies on this mechanism to be able to distribute images.
This may however not be a desirable feature when running a multi-tenant cluster where private images are pulled using credentials. In this scenario, only those pods with the correct credentials would be able to use the image.
Ownership of private images has been an issue for a long time in Kubernetes as indicated by the unresolved issue https://github.com/kubernetes/kubernetes/issues/18787 created back in 2015. The short answer is that a good solution does not exist, with or without Spegel.
The current [suggested solution](https://kubernetes.io/docs/reference/access-authn-authz/admission-controllers/#alwayspullimages) is to enforce an `AlwaysPull` image policy for private images that require authentication. Doing so will force a request to the registry to
validate the digest or resolve the tag. This request will only succeed with the proper authentication. This is a mediocre solution at best as it creates a hard dependency on the external registry, meaning the pod will not be able to start even if the image is cached on the node.

This solution does however not work when using Spegel, instead, Spegel may make the problem worse. Without Spegel an image that would want to use a private image, it does not have access to would have to be scheduled on a node that has already pulled the image.
With Spegel that image will be available to all nodes in the cluster. Currently, a good solution for Spegel does not exist. There are two reasons for this. The first is that credentials are not included when pulling an image from a registry mirror, a good choice as doing so would mean sharing credentials with third parties.
Additionally, Spegel would have no method of validating the credentials even if they were included in the requests. So for the time being if you have these types of requirements Spegel may not be the choice for you.
