set -ex

IMG=$1
CNI=$2
DELETE_E2E_CLUSTER=$3
SCRIPT_PATH=$(realpath $0)
SCRIPT_DIR=$(dirname $SCRIPT_PATH)

TMP_DIR=$(mktemp -d)
KIND_NAME=spegel-e2e
export KIND_KUBECONFIG=$TMP_DIR/kind.kubeconfig
echo $KIND_KUBECONFIG

# Check if kind cluster already exists.
if kind get clusters | grep $KIND_NAME
then
	NEW_CLUSTER=false
else
	NEW_CLUSTER=true
fi

# Either create new cluster or clean existing.
if $NEW_CLUSTER
then
	# Create Kind cluster
	kind create cluster --kubeconfig $KIND_KUBECONFIG --config $SCRIPT_DIR/kind-config-$CNI.yaml --name $KIND_NAME

	# Pull images onto single node which will never run workload.
	docker exec $KIND_NAME-worker ctr -n k8s.io image pull ghcr.io/spegel-org/conformance:75d2816
	docker exec $KIND_NAME-worker ctr -n k8s.io image pull docker.io/library/nginx:1.23.0
	docker exec $KIND_NAME-worker ctr -n k8s.io image pull docker.io/library/nginx@sha256:b3a676a9145dc005062d5e79b92d90574fb3bf2396f4913dc1732f9065f55c4b
	docker exec $KIND_NAME-worker ctr -n k8s.io image pull mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416

	# Write existing configuration to test backup.
	HOSTS_TOML='server = "https://docker.io"\n\n[host."https://registry-1.docker.io"]\n  capabilities = ["push"]'
	docker exec $KIND_NAME-worker2 bash -c "mkdir -p /etc/containerd/certs.d/docker.io; echo -e '$HOSTS_TOML' > /etc/containerd/certs.d/docker.io/hosts.toml"
else
	kind export kubeconfig --kubeconfig $KIND_KUBECONFIG --name $KIND_NAME
	kubectl --kubeconfig $KIND_KUBECONFIG --namespace nginx delete deployments --all
	kubectl --kubeconfig $KIND_KUBECONFIG --namespace conformance delete jobs --all
	helm --kubeconfig $KIND_KUBECONFIG uninstall --ignore-not-found --namespace spegel spegel

	# Delete test images from all expect one node
	for NODE in control-plane worker2 worker3 worker4
	do
		NAME=$KIND_NAME-$NODE
		docker exec $NAME ctr -n k8s.io image rm docker.io/library/nginx:1.21.0@sha256:2f1cd90e00fe2c991e18272bb35d6a8258eeb27785d121aa4cc1ae4235167cfd
		docker exec $NAME ctr -n k8s.io image rm docker.io/library/nginx:1.23.0
		docker exec $NAME ctr -n k8s.io image rm docker.io/library/nginx@sha256:b3a676a9145dc005062d5e79b92d90574fb3bf2396f4913dc1732f9065f55c4b
		docker exec $NAME ctr -n k8s.io image rm mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416
	done

	# Delete Spegel from all nodes
	for NODE in control-plane worker worker2 worker3 worker4
	do
		NAME=$KIND_NAME-$NODE
		docker exec $NAME bash -c "ctr -n k8s.io image ls -q | grep ghcr.io/spegel-org/spegel | xargs ctr -n k8s.io image rm"
		kubectl --kubeconfig $KIND_KUBECONFIG label nodes $NAME spegel=schedule
	done
fi

# Deploy Spegel
kind load docker-image --name $KIND_NAME ${IMG}
DIGEST=$(docker exec $KIND_NAME-worker crictl inspecti -o 'go-template' --template '{{ index .status.repoDigests 0 }}' ${IMG} | cut -d'@' -f2)
for NODE in control-plane worker worker2 worker3 worker4
do
	NAME=$KIND_NAME-$NODE
	docker exec $NAME ctr -n k8s.io image tag ${IMG} ghcr.io/spegel-org/spegel@${DIGEST}
done
helm --kubeconfig $KIND_KUBECONFIG upgrade --create-namespace --wait --install --namespace="spegel" spegel ./charts/spegel --set "image.pullPolicy=Never" --set "image.digest=${DIGEST}" --set "nodeSelector.spegel=schedule"
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel rollout status daemonset spegel --timeout 60s
POD_COUNT=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods --no-headers | wc -l)
if [[ $POD_COUNT != "5" ]]
then
	echo "Spegel should have 5 Pods running."
	exit 1
fi

# Verify that configuration has been backed up.
BACKUP_HOSTS_TOML=$(docker exec $KIND_NAME-worker2 cat /etc/containerd/certs.d/_backup/docker.io/hosts.toml)
if [ $BACKUP_HOSTS_TOML != $HOSTS_TOML ]
then
	echo "Spegel has not properly backed up existing configuration."
	exit 1
fi

# Run conformance tests
kubectl --kubeconfig $KIND_KUBECONFIG create namespace conformance --dry-run=client -o yaml | kubectl --kubeconfig $KIND_KUBECONFIG apply -f -
kubectl --kubeconfig $KIND_KUBECONFIG apply --namespace conformance -f test/e2e/conformance-job.yaml
kubectl --kubeconfig $KIND_KUBECONFIG --namespace conformance wait --for=condition=complete job/conformance

# Remove Spegel from the last node to test that the mirror fallback is working.
SPEGEL_WORKER4=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get pods --no-headers -o name --field-selector spec.nodeName=$KIND_NAME-worker4)
kubectl --kubeconfig $KIND_KUBECONFIG label nodes $KIND_NAME-worker4 spegel-
kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel wait --for=delete $SPEGEL_WORKER4 --timeout=60s

# Verify that both local and external ports are working
HOST_IP=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get nodes $KIND_NAME-worker -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
if ipv6calc --in ipv6addr $HOST_IP; then
	HOST_IP="[${HOST_IP}]"
fi
HTTP_CODE=$(docker exec $KIND_NAME-worker curl -s -o /dev/null -w "%{http_code}" http://${HOST_IP}:30020/healthz || true)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on local port."
	exit 1
fi
HTTP_CODE=$(docker exec $KIND_NAME-worker curl -s -o /dev/null -w "%{http_code}" http://${HOST_IP}:30021/healthz || true)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on external port."
	exit 1
fi
HOST_IP=$(kubectl --kubeconfig $KIND_KUBECONFIG --namespace spegel get nodes $KIND_NAME-worker4 -o jsonpath='{.status.addresses[?(@.type=="InternalIP")].address}')
if ipv6calc --in ipv6addr $HOST_IP; then
	HOST_IP="[${HOST_IP}]"
fi
HTTP_CODE=$(docker exec $KIND_NAME-worker4 curl -s -o /dev/null -w "%{http_code}" http://${HOST_IP}:30020/healthz || true)
if [[ $HTTP_CODE != "000" ]]
then
	echo "Spegel should not be accessible on local port when Spegel is not present on node."
	exit 1
fi
HTTP_CODE=$(docker exec $KIND_NAME-worker4 curl -s -o /dev/null -w "%{http_code}" http://${HOST_IP}:30021/healthz || true)
if [[ $HTTP_CODE != "200" ]]
then
	echo "Spegel should be accessible on external port."
	exit 1
fi

if $NEW_CLUSTER
then
	# Pull images onto single node which will never run workload.
	docker exec $KIND_NAME-worker ctr -n k8s.io image pull docker.io/library/nginx:1.21.0@sha256:2f1cd90e00fe2c991e18272bb35d6a8258eeb27785d121aa4cc1ae4235167cfd

	# Block internet access by only allowing RFC1918 CIDR
	for NODE in control-plane worker worker2 worker3 worker4
	do
		NAME=$KIND_NAME-$NODE
		docker exec $NAME iptables -A OUTPUT -o eth0 -d 10.0.0.0/8 -j ACCEPT
		docker exec $NAME iptables -A OUTPUT -o eth0 -d 172.16.0.0/12 -j ACCEPT
		docker exec $NAME iptables -A OUTPUT -o eth0 -d 192.168.0.0/16 -j ACCEPT
		docker exec $NAME iptables -A OUTPUT -o eth0 -j REJECT
	done
fi

# Pull test image that does not contain any media types
docker exec $KIND_NAME-worker3 crictl pull mcr.microsoft.com/containernetworking/azure-cns@sha256:7944413c630746a35d5596f56093706e8d6a3db0569bec0c8e58323f965f7416

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
kubectl --kubeconfig $KIND_KUBECONFIG label nodes $KIND_NAME-control-plane $KIND_NAME-worker $KIND_NAME-worker2 spegel-
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

if $DELETE_E2E_CLUSTER
then
	# Delete cluster
	kind delete cluster --name $KIND_NAME
	rm -rf $TMP_DIR
fi
