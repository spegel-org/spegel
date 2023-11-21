set -ex

IMG=$1
SCRIPT_PATH=$(realpath $0)
SCRIPT_DIR=$(dirname $SCRIPT_PATH)

# Create Kind cluster
TMP_DIR=$(mktemp -d)
export KIND_KUBECONFIG=$TMP_DIR/kind.kubeconfig
echo $KIND_KUBECONFIG
kind create cluster --kubeconfig $KIND_KUBECONFIG --config $SCRIPT_DIR/kind-config.yaml

# Write existing configuration to test backup.
HOSTS_TOML='server = "https://docker.io"\n\n[host."https://registry-1.docker.io"]\n  capabilities = ["push"]'
docker exec kind-worker2 bash -c "mkdir -p /etc/containerd/certs.d/docker.io; echo -e '$HOSTS_TOML' > /etc/containerd/certs.d/docker.io/hosts.toml"

# Pull images onto single node which will never run workload.
docker exec kind-worker ctr -n k8s.io image pull docker.io/library/nginx:1.23.0
docker exec kind-worker ctr -n k8s.io image pull docker.io/library/nginx@sha256:b3a676a9145dc005062d5e79b92d90574fb3bf2396f4913dc1732f9065f55c4b
docker exec kind-worker ctr -n k8s.io image pull mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416

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

# Verify that configuration has been backed up.
BACKUP_HOSTS_TOML=$(docker exec kind-worker2 cat /etc/containerd/certs.d/_backup/docker.io/hosts.toml)
if [ $BACKUP_HOSTS_TOML != $HOSTS_TOML ]
then
	echo "Spegel has not properly backed up existing configuration."
	exit 1
fi

# Remove Spegel from the last node to test that the mirror fallback is working.
SPEGEL_WORKER4=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods --no-headers -o name --field-selector spec.nodeName=kind-worker4)
kubectl --kubeconfig $KIND_KUBECONFIG label nodes kind-worker4 spegel-
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel wait --for=delete $SPEGEL_WORKER4 --timeout=60s

# Verify that both local and external ports are working
HTTP_CODE=$(docker exec kind-worker curl -s -o /dev/null -w "%{http_code}" http://localhost:30020/healthz)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on local port."
	exit 1
fi
HTTP_CODE=$(docker exec kind-worker curl -s -o /dev/null -w "%{http_code}" http://localhost:30021/healthz)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on external port."
	exit 1
fi
HTTP_CODE=$(docker exec kind-worker4 curl -s -o /dev/null -w "%{http_code}" http://localhost:30020/healthz || true)
if [[ $HTTP_CODE != "000" ]]
then
	echo "Spegel should not be accessible on local port when Spegel is not present on node."
	exit 1
fi
HTTP_CODE=$(docker exec kind-worker4 curl -s -o /dev/null -w "%{http_code}" http://localhost:30021/healthz)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on external port."
	exit 1
fi

# Pull images onto single node which will never run workload.
docker exec kind-worker ctr -n k8s.io image pull docker.io/library/nginx:1.21.0@sha256:2f1cd90e00fe2c991e18272bb35d6a8258eeb27785d121aa4cc1ae4235167cfd

# Block internet access by only allowing RFC1918 CIDR
for NODE in kind-control-plane kind-worker kind-worker2 kind-worker3 kind-worker4
do
	docker exec $NODE iptables -A OUTPUT -o eth0 -d 10.0.0.0/8 -j ACCEPT
	docker exec $NODE iptables -A OUTPUT -o eth0 -d 172.16.0.0/12 -j ACCEPT
	docker exec $NODE iptables -A OUTPUT -o eth0 -d 192.168.0.0/16 -j ACCEPT
	docker exec $NODE iptables -A OUTPUT -o eth0 -j REJECT
done

# Pull test image that does not contain any media types
docker exec kind-worker3 crictl pull mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416

# Deploy test Nginx pods and verify deployment status
kubectl --kubeconfig $KIND_KUBECONFIG apply -f $SCRIPT_DIR/test-nginx.yaml
kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx get pods
kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx wait --timeout=90s deployment/nginx-tag --for condition=available
kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx wait --timeout=90s deployment/nginx-digest --for condition=available
kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx wait --timeout=90s deployment/nginx-tag-and-digest --for condition=available
kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx wait --timeout=90s -l app=nginx-not-present --for jsonpath='{.status.containerStatuses[*].state.waiting.reason}'=ImagePullBackOff pod

# Verify that Spegel has never restarted
RESTART_COUNT=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods -o=jsonpath='{.items[*].status.containerStatuses[0].restartCount}')
if [[ $RESTART_COUNT != "0 0 0 0" ]]
then
	echo "Spegel should not have restarted during tests."
	exit 1
fi

# Remove all Spegel Pods and only restart one to verify that running a single instance works
kubectl --kubeconfig $KIND_KUBECONFIG label nodes kind-control-plane kind-worker kind-worker2 spegel-
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel delete pods --all
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel rollout status daemonset spegel --timeout 60s
POD_COUNT=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods --no-headers | wc -l)
if [[ $POD_COUNT != "1" ]]
then
	echo "Spegel should have 1 Pods running."
	exit 1
fi

# Verify that Spegel has never restarted
RESTART_COUNT=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods -o=jsonpath='{.items[*].status.containerStatuses[0].restartCount}')
if [[ $RESTART_COUNT != "0" ]]
then
	echo "Spegel should not have restarted during tests."
	exit 1
fi

# Delete cluster
kind delete cluster
