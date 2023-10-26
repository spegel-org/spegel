set -ex

IMG=$1
BENCHMARK_CONCURRENCY=$2
BENCHMARK_IMAGE=$3
SCRIPT_PATH=$(realpath $0)
SCRIPT_DIR=$(dirname $SCRIPT_PATH)

# Create Kind cluster
TMP_DIR=$(mktemp -d)
export KIND_KUBECONFIG=$TMP_DIR/kind.kubeconfig
echo $KIND_KUBECONFIG
kind create cluster --kubeconfig $KIND_KUBECONFIG --config $SCRIPT_DIR/kind-config.yaml

# Deploy Spegel
kind load docker-image ${IMG}
DIGEST=$(docker exec kind-worker crictl inspecti -o 'go-template' --template '{{ index .status.repoDigests 0 }}' ${IMG} | cut -d'@' -f2)
for NODE in kind-control-plane kind-worker kind-worker2 kind-worker3 kind-worker4
do
	docker exec $NODE ctr -n k8s.io image tag ${IMG} ghcr.io/xenitab/spegel@${DIGEST}
done
kubectl --kubeconfig $KIND_KUBECONFIG create namespace spegel
helm --kubeconfig $KIND_KUBECONFIG upgrade --wait --install --namespace="spegel" spegel ./charts/spegel --set "image.pullPolicy=Never" --set "image.digest=${DIGEST}" --set "nodeSelector.spegel=schedule"
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel rollout status daemonset spegel --timeout 60s
POD_COUNT=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods --no-headers | wc -l)
if [[ $POD_COUNT != "5" ]]
then
	echo "Spegel should have 5 Pods running."
	exit 1
fi

# Run Benchmark
go run $SCRIPT_DIR/benchmark.go --kubeconfig $KIND_KUBECONFIG --concurrency $BENCHMARK_CONCURRENCY --image ${BENCHMARK_IMAGE}

kind delete cluster