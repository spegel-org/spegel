# Benchmark

The benchmark measures image pull performance in realistic scenarios. The purpose is to validate the expected performance of Spegel and give an indication of the expected performance.
Spegel works best when deploying multiple replicas of an application, as the same image needs to be pulled to multiple nodes resulting in the image being pulled from Spegel.
For this reason, the benchmark consists of two steps. The first step will deploy a daemonset to the cluster. During this step all pods will be deployed at once and all images pulled at the same time.
It tests the worst condition for Spegel as none of the nodes will have the cached image creating a race to pull it first. However, as the image is pulled to the first node it will enable other nodes to pull the image.
The second step updates the daemonset with a new version of the image. Each pod will be replaced one at a time when updating a daemonset. This scenario is better for Spegel.
The first node will need to pull the image from the registry but the second will be able to pull the image from the first node. 
In theory, both of these steps should result in a faster overall image pull time and a similar image pull time from the registry.

## Method

This method describes the process of running the benchmarks on an AKS cluster created with the accompanied Terraform. Replace the kubeconfig to run the benchmark on another cluster.

Start by creating the AKS cluster. A kubeconfig file will be created in the terraform directory after the AKS cluster has been successfully created.

```bash
cd terraform
terraform init
terraform apply
cd ..
```

Run the benchmark without Spegel installed. The first Nginx image is small and has a few layers while the second Plex image is a lot larger. The benchmark will output the path to a directory containing the results.

```bash
go run benchmark.go benchmark --result-dir ./results --name nginx-without-spegel --kubeconfig ./terraform/benchmark.kubeconfig --namespace spegel-benchmark --images ghcr.io/mirrorshub/docker/nginx:1.24-alpine ghcr.io/mirrorshub/docker/nginx:1.25-alpine
go run benchmark.go benchmark --result-dir ./results --name plex-without-spegel  --kubeconfig ./terraform/benchmark.kubeconfig --namespace spegel-benchmark --images ghcr.io/linuxserver/plex:1.31.0 ghcr.io/linuxserver/plex:1.32.0
```

Deploy Spegel in the cluster and wait for all of the pods to run.

```bash
export KUBECONFIG=$(pwd)/terraform/benchmark.kubeconfig
helm upgrade --create-namespace --namespace spegel --install --version $VERSION spegel oci://ghcr.io/spegel-org/helm-charts/spegel
kubectl --namespace spegel rollout status daemonset spegel --timeout 60s
```

Run the same benchmarks as before, now with Spegel installed.

```bash
go run benchmark.go benchmark --result-dir ./results --name nginx-with-spegel --kubeconfig ./terraform/benchmark.kubeconfig --namespace spegel-benchmark --images ghcr.io/mirrorshub/docker/nginx:1.24-alpine ghcr.io/mirrorshub/docker/nginx:1.25-alpine
go run benchmark.go benchmark --result-dir ./results --name plex-with-spegel --kubeconfig ./terraform/benchmark.kubeconfig --namespace spegel-benchmark --images ghcr.io/linuxserver/plex:1.31.0 ghcr.io/linuxserver/plex:1.32.0
```

Destroy the AKS cluster as it is no longer needed.

```bash
cd terraform
terraform destroy
cd ..
```
