#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

if [[ ${E2E_MANAGED_SQLITE_WORKER:-} != 1 || $# != 3 || ! ${1:-} =~ ^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$ ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_SQLITE_WORKER=1 bash hack/e2e-managed-sqlite-worker.sh <isolated-guest-kube-system-uid> <sqlite-source.json> <worker-assets-dir>' >&2
  exit 2
fi

export KUBECONFIG=/var/lib/k0s/pki/admin.conf
if [[ $(uname -sm) != 'Linux x86_64' || ! -s /var/lib/bedrock/e2e-admin-password || ! -f $KUBECONFIG || -L $KUBECONFIG ]]; then
  printf '%s\n' 'Run this fixture inside an initialized amd64 Bedrock development guest.' >&2
  exit 1
fi
if [[ $(kubectl --request-timeout=10s get namespace kube-system -o jsonpath='{.metadata.uid}') != "$1" ]]; then
  printf '%s\n' 'The API does not match the approved isolated guest.' >&2
  exit 1
fi

script_dir=$(dirname "${BASH_SOURCE[0]}")
source "$script_dir/e2e/lib.sh"

[[ -f $2 && ! -L $2 && -O $2 ]]
source_receipt=$(realpath -e -- "$2")
assets=$3
namespace=bedrock-e2e-sqlite-source
cluster=sqlite-source
service=sqlite-endpoint
worker=sqlite-worker
root_disk=$worker-root
release=k0s-v1.36.3+k0s.1
busybox=docker.io/library/busybox:1.37.0

[[ $assets =~ ^/[a-zA-Z0-9._/-]+$ && $(realpath -e "$assets") == "$assets" && -O $assets && $(stat -c '%a' "$assets") == 700 ]]
for file in "$source_receipt" "$assets/assets.json" "$assets/k0s" "$assets/k0s-airgap.tar" "$assets/busybox.tar"; do
  [[ -f $file && ! -L $file && -O $file && -s $file ]]
done
for file in "$assets/k0s" "$assets/k0s-airgap.tar" "$assets/busybox.tar"; do
  [[ $(stat -c '%a' "$file") =~ ^(444|644)$ ]]
done

asset_receipt=$assets/assets.json
jq -e --arg version "${release#k0s-}" '
  keys == ["airgap_sha256","busybox_digest","busybox_tar_sha256","k0s_sha256","k0s_version","ubuntu_digest"] and
  .k0s_version == $version and ([.airgap_sha256,.busybox_tar_sha256,.k0s_sha256] | all(.[]; type == "string" and test("^[0-9a-f]{64}$")))
' "$asset_receipt" >/dev/null
jq -e '[.busybox_digest,.ubuntu_digest] | all(.[]; type == "string" and test("^sha256:[0-9a-f]{64}$"))' "$asset_receipt" >/dev/null
k0s_sha=$(jq -er '.k0s_sha256' "$asset_receipt")
airgap_sha=$(jq -er '.airgap_sha256' "$asset_receipt")
busybox_sha=$(jq -er '.busybox_tar_sha256' "$asset_receipt")
busybox_digest=$(jq -er '.busybox_digest' "$asset_receipt")
ubuntu_digest=$(jq -er '.ubuntu_digest' "$asset_receipt")
printf '%s  %s\n' "$k0s_sha" "$assets/k0s" "$airgap_sha" "$assets/k0s-airgap.tar" "$busybox_sha" "$assets/busybox.tar" | sha256sum -c --status
proof_dir=${E2E_MANAGED_SQLITE_WORKER_PROOF_DIR:-"$(dirname -- "$source_receipt")/worker-prejoin"}
proof_parent=$(dirname -- "$proof_dir")
[[ $proof_dir == /* && $(realpath -e -- "$proof_parent") == "$proof_parent" && -O $proof_parent &&
  $(stat -c '%a' -- "$proof_parent") == 700 ]]
mkdir -m 0700 -- "$proof_dir"
install -m 0600 -- "$source_receipt" "$proof_dir/source.json"
install -m 0600 -- "$asset_receipt" "$proof_dir/assets.json"
source_sum=$(sha256sum -- "$proof_dir/source.json")
asset_sum=$(sha256sum -- "$proof_dir/assets.json")

service_record=$(kubectl -n "$namespace" get service "$service" -o json)
address=$(jq -er .spec.clusterIP <<<"$service_record")
service_uid=$(jq -er '.metadata.uid | select(length>0)' <<<"$service_record")
[[ $address =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
endpoint=https://$address:6443
cluster_uid=$(kubectl -n "$namespace" get "cluster.k0smotron.io/$cluster" -o json | jq -er --arg address "$address" --arg version "${release#k0s-}" '
  select(.spec.version == $version and .spec.replicas == 1 and .status.readyReplicas == 1 and .spec.externalAddress == $address and
    .spec.storage.type == "kine" and .spec.storage.kine.dataSourceURL == "sqlite:///var/lib/k0s/state.db" and
    (.spec.patches // []) == [] and (.spec.persistence.autoDeletePVCs // false) == false) | .metadata.uid | select(length > 0)
')
namespace_uid=$(kubectl get namespace "$namespace" -o json | jq -er '
  select(.status.phase == "Active" and (.metadata.labels["bedrock.cloudyfolks.io/project"] == null) and
    (.metadata.labels["bedrock.cloudyfolks.io/tenant"] == null)) | .metadata.uid | select(length > 0)
')
kubectl -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items == []' >/dev/null
jq -e --arg ns "$namespace" --arg ns_uid "$namespace_uid" --arg cluster "$cluster" --arg uid "$cluster_uid" \
  --arg endpoint "$endpoint" --arg tunnel "$address:8132" --arg service "$service" '
    .namespace == $ns and .namespace_uid == $ns_uid and .cluster == $cluster and .cluster_uid == $uid and
    .service == $service and .api_endpoint == $endpoint and .konnectivity_endpoint == $tunnel and .marker == "bedrock-sqlite-continuity" and
    .statefulset == "kmc-sqlite-source" and .claim == "kmc-sqlite-source-kmc-sqlite-source-0"
  ' "$source_receipt" >/dev/null
source_claim_uid=$(jq -er '.claim_uid | select(length > 0)' "$source_receipt")
source_volume=$(kubectl -n "$namespace" get pvc kmc-sqlite-source-kmc-sqlite-source-0 -o json | jq -er --arg uid "$source_claim_uid" '
  select(.metadata.uid == $uid and .status.phase == "Bound") | .spec.volumeName | select(length > 0)
')
expect_equal "$(jq -er .volume "$source_receipt")" "$source_volume" 'controller volume name' >&2
expect_equal "$(jq -er .volume_uid "$source_receipt")" "$(kubectl get pv "$source_volume" -o jsonpath='{.metadata.uid}')" 'controller volume UID' >&2
kubectl -n "$namespace" get service "$service" -o json | jq -e '
  .spec.type == "ClusterIP" and (.spec.externalIPs // []) == [] and
  .spec.selector == {"statefulset.kubernetes.io/pod-name":"kmc-sqlite-source-0"} and
  ([.spec.ports[] | [.port,.targetPort,.protocol]] | sort) == [[6443,6443,"TCP"],[8132,8132,"TCP"]]
' >/dev/null
kubectl -n "$namespace" get networkpolicy sqlite-source-ingress -o json | jq -e '
  .spec.podSelector.matchLabels == {"statefulset.kubernetes.io/pod-name":"kmc-sqlite-source-0"} and
  [.spec.ingress[].from[]] == [{podSelector:{}}] and ([.spec.ingress[].ports[] | [.port,.protocol]] | sort) == [[6443,"TCP"],[8132,"TCP"]]
' >/dev/null

child_kubectl() {
  kubectl -n "$namespace" exec -i kmc-sqlite-source-0 -c controller -- k0s kubectl --kubeconfig=/var/lib/k0s/pki/admin.conf \
    --server=https://127.0.0.1:6443 --tls-server-name="$address" --request-timeout=15s "$@"
}

expect_equal "$(jq -er .marker_uid "$source_receipt")" \
  "$(child_kubectl -n default get configmap bedrock-sqlite-continuity -o jsonpath='{.metadata.uid}')" 'original SQLite marker UID' >&2
host_node=$(kubectl get nodes -o json | jq -er '
  select(.items | length == 1) | .items[0] |
  select(any(.status.conditions[]; .type == "Ready" and .status == "True")) | .metadata.name
')
ubuntu_source=$(kubectl get clusterimages.bedrock.cloudyfolks.io ubuntu-24.04 -o json |
  jq -er --arg digest "$ubuntu_digest" '
    select(.status.phase == "Ready" and .status.observedGeneration == .metadata.generation and .status.digest == $digest and
      .spec.os.family == "ubuntu" and .spec.os.version == "24.04" and .spec.os.architecture == "amd64") |
    .status.dataSource | select(test("^[a-z0-9][a-z0-9.-]*$"))')
ubuntu_source_uid=$(kubectl -n bedrock-images get datasources.cdi.kubevirt.io "$ubuntu_source" -o jsonpath='{.metadata.uid}')
test -n "$ubuntu_source_uid"

k0s ctr --namespace k8s.io images import "$assets/busybox.tar" >/dev/null
k0s ctr --namespace k8s.io images ls | awk -v image="$busybox" -v digest="$busybox_digest" '$1 == image && $3 == digest { match_count++ } END { exit match_count != 1 }'

kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: v1
kind: Pod
metadata:
  name: sqlite-worker-assets
  labels:
    app: sqlite-worker-assets
spec:
  nodeName: $host_node
  automountServiceAccountToken: false
  containers:
    - name: http
      image: $busybox
      imagePullPolicy: Never
      command: [busybox, httpd, -f, -p, "8080", -h, /files]
      readinessProbe:
        tcpSocket:
          port: 8080
      securityContext:
        runAsNonRoot: true
        runAsUser: 65534
        allowPrivilegeEscalation: false
        capabilities:
          drop: [ALL]
      volumeMounts:
        - {name: k0s, mountPath: /files/k0s, readOnly: true}
        - {name: airgap, mountPath: /files/k0s-airgap.tar, readOnly: true}
        - {name: workload, mountPath: /files/busybox.tar, readOnly: true}
  volumes:
    - name: k0s
      hostPath: {path: "$assets/k0s", type: File}
    - name: airgap
      hostPath: {path: "$assets/k0s-airgap.tar", type: File}
    - name: workload
      hostPath: {path: "$assets/busybox.tar", type: File}
---
apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: sqlite-worker-assets
spec:
  podSelector:
    matchLabels: {app: sqlite-worker-assets}
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector: {}
      ports:
        - {port: 8080, protocol: TCP}
---
apiVersion: v1
kind: Service
metadata:
  name: sqlite-worker-assets
spec:
  selector: {app: sqlite-worker-assets}
  ports:
    - {port: 8080, targetPort: 8080, protocol: TCP}
---
apiVersion: cdi.kubevirt.io/v1beta1
kind: DataVolume
metadata:
  name: $root_disk
spec:
  sourceRef:
    kind: DataSource
    namespace: bedrock-images
    name: $ubuntu_source
  storage:
    storageClassName: block
    volumeMode: Block
    accessModes: [ReadWriteOnce]
    resources:
      requests:
        storage: 20Gi
EOF

kubectl -n "$namespace" wait --for=condition=Ready pod/sqlite-worker-assets --timeout=180s >&2
asset_ip=$(kubectl -n "$namespace" get service sqlite-worker-assets -o jsonpath='{.spec.clusterIP}')
[[ $asset_ip =~ ^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$ ]]
wait_jsonpath 900 Succeeded -n "$namespace" get datavolume "$root_disk" -o jsonpath='{.status.phase}'
expect_equal "$ubuntu_source_uid" "$(kubectl -n bedrock-images get datasources.cdi.kubevirt.io "$ubuntu_source" -o jsonpath='{.metadata.uid}')" 'Ubuntu data source UID' >&2
root_claim=$(kubectl -n "$namespace" get pvc "$root_disk" -o json)
jq -e '.status.phase == "Bound" and .spec.volumeMode == "Block" and .spec.storageClassName == "block" and .spec.accessModes == ["ReadWriteOnce"]' <<<"$root_claim" >/dev/null
root_claim_uid=$(jq -er '.metadata.uid | select(length > 0)' <<<"$root_claim")
root_volume=$(jq -er '.spec.volumeName | select(length > 0)' <<<"$root_claim")
root_volume_uid=$(kubectl get pv "$root_volume" -o json | jq -er --arg ns "$namespace" --arg claim "$root_disk" --arg uid "$root_claim_uid" '
  select(.status.phase == "Bound" and .spec.volumeMode == "Block" and .spec.csi.driver == "rook-ceph.rbd.csi.ceph.com" and
    (.spec.claimRef | .namespace == $ns and .name == $claim and .uid == $uid)) | .metadata.uid | select(length > 0)
')

kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: k0smotron.io/v1beta2
kind: JoinTokenRequest
metadata:
  name: $worker
spec:
  clusterName: $cluster
  expiry: 90m
  role: worker
EOF
wait_not_empty 180 -n "$namespace" get jointokenrequest.k0smotron.io "$worker" -o jsonpath='{.status.tokenID}'
join_request_uid=$(kubectl -n "$namespace" get jointokenrequest.k0smotron.io "$worker" -o jsonpath='{.metadata.uid}')
join_secret_uid=$(kubectl -n "$namespace" get secret "$worker" -o jsonpath='{.metadata.uid}')
test -n "$join_request_uid" && test -n "$join_secret_uid"
token=$(kubectl -n "$namespace" get secret "$worker" -o jsonpath='{.data.token}')
[[ $token =~ ^[A-Za-z0-9+/=]+$ ]]
printf '%s' "$token" | base64 -d | base64 -d | gzip -d | awk -v endpoint="$endpoint" '
  $1 == "server:" { count++; if ($2 == endpoint) matched++ }
  END { exit !(count == 1 && matched == 1) }
'

kubectl -n "$namespace" create secret generic "$worker-bootstrap" --from-file=userdata=/dev/stdin >&2 <<EOF
#cloud-config
hostname: $worker
preserve_hostname: false
write_files:
  - path: /etc/k0s/join.token
    owner: root:root
    permissions: "0600"
    encoding: b64
    content: $token
  - path: /usr/local/sbin/join-sqlite-worker
    owner: root:root
    permissions: "0700"
    content: |
      set -eu
      install -d -m 0755 /var/lib/k0s/images /var/lib/sqlite-continuity
      printf '%s\n' 'Downloading the k0s binary.'
      curl -fsS --retry 4 --connect-timeout 10 --max-time 900 http://$asset_ip:8080/k0s -o /usr/local/bin/k0s
      printf '%s\n' 'Downloading the k0s airgap images.'
      curl -fsS --retry 4 --connect-timeout 10 --max-time 900 http://$asset_ip:8080/k0s-airgap.tar -o /var/lib/k0s/images/k0s-airgap.tar
      printf '%s\n' 'Downloading the worker workload image.'
      curl -fsS --retry 4 --connect-timeout 10 --max-time 300 http://$asset_ip:8080/busybox.tar -o /var/lib/k0s/images/busybox.tar
      printf '%s  %s\n' '$k0s_sha' /usr/local/bin/k0s '$airgap_sha' /var/lib/k0s/images/k0s-airgap.tar '$busybox_sha' /var/lib/k0s/images/busybox.tar | sha256sum -c --status
      chmod 0755 /usr/local/bin/k0s
      /usr/local/bin/k0s version | grep -Fx '${release#k0s-}'
      printf '%s\n' 'sqlite-worker-data-before-handoff' > /var/lib/sqlite-continuity/marker
      sync
      printf '%s\n' 'Installing the k0s worker.'
      /usr/local/bin/k0s install worker --token-file=/etc/k0s/join.token
      /usr/local/bin/k0s start
      printf '%s\n' 'Started the k0s worker service.'
runcmd:
  - [/bin/sh, /usr/local/sbin/join-sqlite-worker]
EOF
unset token
bootstrap_uid=$(kubectl -n "$namespace" get secret "$worker-bootstrap" -o jsonpath='{.metadata.uid}')
test -n "$bootstrap_uid"

firmware_uid=$(cat /proc/sys/kernel/random/uuid)
mac_seed=$(printf '%s' "$cluster_uid" | sha256sum)
mac=02:${mac_seed:0:2}:${mac_seed:2:2}:${mac_seed:4:2}:${mac_seed:6:2}:${mac_seed:8:2}
kubectl -n "$namespace" create -f - >&2 <<EOF
apiVersion: kubevirt.io/v1
kind: VirtualMachine
metadata:
  name: $worker
spec:
  runStrategy: Always
  template:
    spec:
      hostname: $worker
      domain:
        cpu: {cores: 2}
        resources:
          requests: {memory: 4Gi}
        firmware:
          uuid: $firmware_uid
        devices:
          disks:
            - name: root
              disk: {bus: virtio}
              bootOrder: 1
              serial: sqlite-worker-root
            - name: bootstrap
              disk: {bus: virtio}
          interfaces:
            - name: default
              masquerade: {}
              macAddress: "$mac"
      networks:
        - name: default
          pod: {}
      volumes:
        - name: root
          persistentVolumeClaim: {claimName: $root_disk}
        - name: bootstrap
          cloudInitNoCloud:
            secretRef: {name: $worker-bootstrap}
EOF

wait_jsonpath 900 Running -n "$namespace" get virtualmachineinstance.kubevirt.io "$worker" -o jsonpath='{.status.phase}'
vm_uid=$(kubectl -n "$namespace" get virtualmachine.kubevirt.io "$worker" -o jsonpath='{.metadata.uid}')
vmi_uid=$(kubectl -n "$namespace" get virtualmachineinstance.kubevirt.io "$worker" -o json |
  jq -er --arg vm "$vm_uid" --arg firmware "$firmware_uid" --arg mac "$mac" --arg root "$root_disk" '
    select(any(.metadata.ownerReferences[]; .kind == "VirtualMachine" and .uid == $vm and .controller == true) and
      .spec.domain.firmware.uuid == $firmware and any(.spec.domain.devices.interfaces[]; .name == "default" and .macAddress == $mac) and
      any(.spec.domain.devices.disks[]; .name == "root" and .serial == "sqlite-worker-root") and
      any(.spec.volumes[]; .name == "root" and .persistentVolumeClaim.claimName == $root)) |
    .metadata.uid | select(length > 0)')
test -n "$vm_uid" && test -n "$vmi_uid"
expect_equal "$root_claim_uid" "$(kubectl -n "$namespace" get pvc "$root_disk" -o jsonpath='{.metadata.uid}')" 'worker root claim UID' >&2
expect_equal "$root_volume_uid" "$(kubectl get pv "$root_volume" -o jsonpath='{.metadata.uid}')" 'worker root volume UID' >&2
expect_equal "$service_uid" "$(kubectl -n "$namespace" get service "$service" -o jsonpath='{.metadata.uid}')" 'SQLite source Service UID' >&2
prejoin=$proof_dir/worker-prejoin.json
jq -n --arg guest "$1" --arg source_sha "${source_sum%% *}" --arg assets_sha "${asset_sum%% *}" \
  --arg ns "$namespace_uid" --arg cluster "$cluster_uid" --arg service "$service_uid" --arg vm "$vm_uid" --arg vmi "$vmi_uid" \
  --arg claim "$root_claim_uid" --arg volume "$root_volume" --arg pv "$root_volume_uid" --arg mac "$mac" --arg firmware "$firmware_uid" \
  --arg request "$join_request_uid" --arg token "$join_secret_uid" --arg bootstrap "$bootstrap_uid" '
  {version:1,scope:"synthetic-sqlite-worker-prejoin",guest_uid:$guest,source_receipt_sha256:$source_sha,assets_receipt_sha256:$assets_sha,
   namespace_uid:$ns,source_cluster_uid:$cluster,service_uid:$service,vm_uid:$vm,vmi_uid:$vmi,root_claim_uid:$claim,root_volume:$volume,
   root_volume_uid:$pv,mac_address:$mac,firmware_uuid:$firmware,join_token_request_uid:$request,join_token_secret_uid:$token,
   bootstrap_secret_uid:$bootstrap}' >"$proof_dir/prejoin.tmp"
ln -T -- "$proof_dir/prejoin.tmp" "$prejoin"
rm -- "$proof_dir/prejoin.tmp"
prejoin_sum=$(sha256sum -- "$prejoin")
printf 'Saved private worker inputs in %s. Pre-join SHA-256 is %s.\n' "$proof_dir" "${prejoin_sum%% *}" >&2
E2E_MANAGED_SQLITE_WORKER_CONFIRM=1 bash "$script_dir/e2e-managed-sqlite-worker-confirm.sh" "$1" "$proof_dir/source.json" \
  "$proof_dir/assets.json" "$prejoin" "${prejoin_sum%% *}"
