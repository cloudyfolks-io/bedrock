#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077

fail() {
  printf '%s\n' "$1" >&2
  exit 1
}

uid_pattern='^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$'
sha_pattern='^[0-9a-f]{64}$'
if [[ ${E2E_MANAGED_ETCD_API_EVIDENCE:-} != 1 || $# != 4 || ! ${1:-} =~ $uid_pattern || ! ${3:-} =~ $sha_pattern ]]; then
  printf '%s\n' 'Usage: E2E_MANAGED_ETCD_API_EVIDENCE=1 bash hack/e2e-managed-etcd-api-evidence.sh <isolated-guest-uid> <private-source.json> <approved-source-sha256> <empty-private-output-directory>' >&2
  exit 2
fi

guest_uid=$1
source_path=$2
source_sha=$3
out=$4
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
[[ $(uname -s) == Linux && -s /var/lib/bedrock/e2e-admin-password && -f $KUBECONFIG && ! -L $KUBECONFIG ]] ||
  fail 'Run this fixture inside an initialized Bedrock development guest.'
for tool in kubectl jq python3 openssl base64 sha256sum realpath stat find mktemp timeout ln; do
  command -v "$tool" >/dev/null
done
[[ $(kubectl --request-timeout=10s get namespace kube-system -o jsonpath='{.metadata.uid}') == "$guest_uid" ]] ||
  fail 'The API does not match the approved isolated guest.'
[[ $out == /* && -d $out && -O $out && $(realpath -e -- "$out") == "$out" && $(stat -c '%a' -- "$out") == 700 ]] ||
  fail 'Use an absolute mode-0700 output directory that you own.'
entries=$(find "$out" -mindepth 1 -maxdepth 1 -print -quit)
[[ -z $entries ]] || fail 'Use an empty output directory.'
work=$(mktemp -d "$out/.etcd-api.XXXXXX")
probe_name=
probe_uid=
remove_probe() {
  [[ -n $probe_name && -n $probe_uid ]] || return 0
  jq -nc --arg uid "$probe_uid" '{apiVersion:"v1",kind:"DeleteOptions",preconditions:{uid:$uid}}' |
    kubectl --request-timeout=10s delete --raw "/api/v1/namespaces/$namespace/pods/$probe_name" -f - >/dev/null 2>&1 || return 1
  probe_name=
  probe_uid=
}
cleanup() {
  local result=$?
  if ! remove_probe; then
    printf '%s\n' 'The isolated API probe cleanup could not be verified.' >&2
    result=1
  fi
  if [[ "$out/original.kubeconfig" -ef "$work/original.kubeconfig" && ! "$out/api-evidence.json" -ef "$work/api-evidence.json" ]]; then
    rm -f -- "$out/original.kubeconfig"
  fi
  rm -rf -- "$work"
  exit "$result"
}
trap cleanup EXIT

approved=$work/source.json
python3 -I - "$source_path" "$source_sha" "$approved" <<'PY'
import hashlib
import ipaddress
import json
import os
import stat
import sys

def unique(items):
    result = dict(items)
    if len(result) != len(items):
        raise ValueError()
    return result

source, digest, target = sys.argv[1:]
try:
    if not os.path.isabs(source):
        raise ValueError()
    with os.fdopen(os.open(source, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK), "rb") as handle:
        info = os.fstat(handle.fileno())
        if not stat.S_ISREG(info.st_mode) or stat.S_IMODE(info.st_mode) != 0o600 or info.st_uid != os.getuid():
            raise ValueError()
        data = handle.read(1024 * 1024 + 1)
    if not data or len(data) > 1024 * 1024 or hashlib.sha256(data).hexdigest() != digest:
        raise ValueError()
    receipt = json.loads(data, object_pairs_hook=unique)
    address = ipaddress.IPv4Address(receipt["api_service"]["address"])
    if address.is_unspecified or address.is_loopback or address.is_multicast or address.is_link_local:
        raise ValueError()
    with open(target, "xb") as output:
        output.write(data)
except (OSError, ValueError, KeyError, TypeError, RecursionError):
    sys.exit("Use the approved private receipt for the original Service.")
PY

namespace=bedrock-e2e-etcd-source
cluster=etcd-source
service=etcd-endpoint
controller_set=kmc-etcd-source
controller_pod=kmc-etcd-source-0
controller_image=quay.io/k0sproject/k0s:v1.36.3-k0s.1
marker=bedrock-etcd-continuity
endpoint=$(jq -er --arg guest "$guest_uid" --arg ns "$namespace" --arg cluster "$cluster" --arg service "$service" \
  --arg set "$controller_set" --arg pod "$controller_pod" --arg marker "$marker" --arg uid "$uid_pattern" '
  select(.version==1 and .guest_uid==$guest and .namespace==$ns and .cluster==$cluster and .k0s_version=="v1.36.3+k0s.1" and
    .api_service.name==$service and .api_endpoint==("https://" + .api_service.address + ":6443") and
    .konnectivity_endpoint==(.api_service.address + ":8132") and .marker.name==$marker and
    (.marker.revision | test("^[1-9][0-9]*$")) and
    .controller.statefulset==$set and .controller.pod==$pod and .controller.disk.claim==($set+"-"+$pod) and
    (.controller.image_id | test("sha256:[0-9a-f]{64}$")) and
    ([.guest_uid,.namespace_uid,.cluster_uid,.api_service.uid,.marker.uid,.controller.statefulset_uid,.controller.pod_uid] |
      all(.[]; test($uid) and .!="00000000-0000-0000-0000-000000000000") and length==(unique|length))) | .api_endpoint' "$approved") ||
  fail 'The source receipt does not match the isolated fixture.'
cluster_uid=$(jq -er .cluster_uid "$approved")

kc() {
  kubectl --request-timeout=15s "$@"
}

file_hash() {
  local result
  result=$(sha256sum -- "$1") || return 1
  printf '%s\n' "${result%% *}"
}

source_identity() {
  kc get namespace "$namespace" -o json | jq -e --slurpfile source "$approved" '
    .metadata.uid==$source[0].namespace_uid and .metadata.deletionTimestamp==null and .status.phase=="Active" and
    .metadata.labels["bedrock.cloudyfolks.io/tenant"]==null and .metadata.labels["bedrock.cloudyfolks.io/project"]==null' >/dev/null &&
  kc -n "$namespace" get rolebindings.rbac.authorization.k8s.io -o json | jq -e '.items==[]' >/dev/null &&
  kc get --raw "/apis/k0smotron.io/v1beta2/namespaces/$namespace/clusters/$cluster" | jq -e --slurpfile source "$approved" '
    .metadata.uid==$source[0].cluster_uid and .metadata.deletionTimestamp==null and .spec.replicas==1 and
    .metadata.labels["app.kubernetes.io/managed-by"]=="cluster-api" and .spec.version==$source[0].k0s_version and
    .spec.externalAddress==$source[0].api_service.address and .spec.storage.type=="etcd" and
    .spec.storage.etcd.image=="quay.io/k0sproject/etcd:v3.7.1"' >/dev/null &&
  kc -n "$namespace" get statefulset "$controller_set" -o json | jq -e --slurpfile source "$approved" '
    $source[0] as $s |
    .metadata.uid==$s.controller.statefulset_uid and .metadata.deletionTimestamp==null and .spec.replicas==1 and
    (.metadata.ownerReferences | length==1 and any(.[];
      .apiVersion=="k0smotron.io/v1beta2" and .kind=="Cluster" and .name==$s.cluster and .uid==$s.cluster_uid and .controller==true))' >/dev/null
}

controller_identity() {
  kc -n "$namespace" get pod "$controller_pod" -o json | jq -ce --slurpfile source "$approved" --arg image "$controller_image" '
    $source[0].controller as $c |
    select(.metadata.uid==$c.pod_uid and .metadata.deletionTimestamp==null and .status.phase=="Running" and
      (.spec.hostNetwork // false)==false and (.status.podIP | test("^[0-9]+([.][0-9]+){3}$")) and
      (.metadata.ownerReferences | length==1 and any(.[];
        .apiVersion=="apps/v1" and .kind=="StatefulSet" and .name==$c.statefulset and .uid==$c.statefulset_uid and .controller==true)) and
      any(.spec.volumes[]; .name==$c.statefulset and .persistentVolumeClaim.claimName==$c.disk.claim) and
      ((.spec.ephemeralContainers // []) | length==0) and (.spec.containers | length==1 and .[0].name=="controller" and .[0].image==$image) and
      (.spec.nodeName | type=="string" and length>0) and
      any(.status.conditions[]; .type=="Ready" and .status=="True") and
      (.status.containerStatuses | length==1 and any(.[];
        .name=="controller" and .ready==true and .state.running!=null and .imageID==$c.image_id and (.containerID | length)>0))) |
    {ip:.status.podIP,node:.spec.nodeName,container_id:.status.containerStatuses[0].containerID,image_id:.status.containerStatuses[0].imageID}'
}

start_probe() {
  local created node
  node=$(jq -er .node <<<"$controller_before") || return 1
  created=$(jq -nc --arg image "$controller_image" --arg node "$node" '
    {apiVersion:"v1",kind:"Pod",metadata:{generateName:"etcd-api-probe-",labels:{"migration.cloudyfolks.io/purpose":"etcd-api-probe"}},
     spec:{nodeName:$node,automountServiceAccountToken:false,enableServiceLinks:false,restartPolicy:"Never",activeDeadlineSeconds:180,
       containers:[{name:"client",image:$image,imagePullPolicy:"Never",command:["/bin/sh","-c","exec sleep 180"],
                    securityContext:{allowPrivilegeEscalation:false,capabilities:{drop:["ALL"]}}}]}}' |
    kc -n "$namespace" create -f - -o json 2>/dev/null) || return 1
  probe_name=$(jq -er '.metadata.name | select(test("^etcd-api-probe-[a-z0-9]+$"))' <<<"$created") || return 1
  probe_uid=$(jq -er --arg uid "$uid_pattern" '.metadata.uid | select(test($uid) and .!="00000000-0000-0000-0000-000000000000")' <<<"$created") || return 1
  timeout 75s kubectl --request-timeout=70s -n "$namespace" wait --for=condition=Ready "pod/$probe_name" --timeout=65s >/dev/null 2>&1
}

probe_identity() {
  kc -n "$namespace" get pod "$probe_name" -o json 2>/dev/null |
    jq -ce --arg uid "$probe_uid" --arg ns "$namespace" --arg name "$probe_name" --arg image "$controller_image" \
      --argjson backend "$controller_before" '
      select(.metadata.uid==$uid and .metadata.name==$name and .metadata.namespace==$ns and .metadata.deletionTimestamp==null and
        .metadata.labels=={"migration.cloudyfolks.io/purpose":"etcd-api-probe"} and (.metadata.ownerReferences // [])==[] and
        .status.phase=="Running" and (.status.podIP | test("^[0-9]+([.][0-9]+){3}$") and .!=$backend.ip) and
        .spec.nodeName==$backend.node and .spec.automountServiceAccountToken==false and .spec.enableServiceLinks==false and
        .spec.restartPolicy=="Never" and .spec.activeDeadlineSeconds==180 and
        ([.spec.hostNetwork,.spec.hostPID,.spec.hostIPC] | all(.[]; (. // false)==false)) and
        ([.spec.volumes,.spec.initContainers,.spec.ephemeralContainers] | all(.[]; (. // [])==[])) and
        (.spec.containers | length==1 and all(.[]; .name=="client" and .image==$image and .imagePullPolicy=="Never" and
          .command==["/bin/sh","-c","exec sleep 180"] and ([.args,.env,.envFrom,.volumeMounts,.volumeDevices,.ports] | all(.[]; (. // [])==[])) and
          .securityContext=={allowPrivilegeEscalation:false,capabilities:{drop:["ALL"]}})) and
        (.status.containerStatuses | length==1 and all(.[]; .name=="client" and .ready==true and .state.running!=null and
          .restartCount==0 and .imageID==$backend.image_id and (.containerID | length>0)))) |
      {ip:.status.podIP,container_id:.status.containerStatuses[0].containerID}' 2>/dev/null
}

service_identity() {
  kc -n "$namespace" get service "$service" -o json | jq -ce --slurpfile source "$approved" '
    $source[0] as $s |
    select(.metadata.uid==$s.api_service.uid and .metadata.deletionTimestamp==null and (.metadata.ownerReferences // [])==[] and
      .spec.type=="ClusterIP" and .spec.clusterIP==$s.api_service.address and .spec.clusterIPs==[$s.api_service.address] and
      (.spec.externalIPs // [])==[] and (.spec.externalName // "")=="" and (.status.loadBalancer.ingress // [])==[] and
      (.spec.publishNotReadyAddresses // false)==false and .spec.selector=={"statefulset.kubernetes.io/pod-name":$s.controller.pod} and
      ([.spec.ports[] | [.name,.port,.targetPort,.protocol,(.nodePort // 0)]] | sort)==
        [["api",6443,6443,"TCP",0],["konnectivity",8132,8132,"TCP",0]]) | .metadata.resourceVersion | select(length>0)'
}

endpoints_match() {
  kc -n "$namespace" get endpointslices.discovery.k8s.io -l "kubernetes.io/service-name=$service" -o json |
    jq -e --slurpfile source "$approved" --arg ip "$1" '
      $source[0] as $s |
      (.metadata.continue // "")=="" and (.items | length==1 and all(.[];
        .metadata.namespace==$s.namespace and .metadata.deletionTimestamp==null and .addressType=="IPv4" and
        (.metadata.ownerReferences | length==1 and any(.[];
          .apiVersion=="v1" and .kind=="Service" and .name==$s.api_service.name and .uid==$s.api_service.uid and .controller==true)) and
        ([.ports[] | [.name,.port,.protocol]] | sort)==[["api",6443,"TCP"],["konnectivity",8132,"TCP"]] and
        (.endpoints | length==1 and all(.[];
          .addresses==[$ip] and .conditions.ready==true and (.conditions.terminating // false)==false and
          (.targetRef | .kind=="Pod" and .namespace==$s.namespace and .name==$s.controller.pod and .uid==$s.controller.pod_uid)))))' >/dev/null
}

source_secret() {
  kc -n "$namespace" get secret "$1" -o json 2>/dev/null |
    jq -ce --arg name "$1" --arg field "$2" --arg ns "$namespace" --arg cluster "$cluster" --arg uid "$cluster_uid" --arg pattern "$uid_pattern" '
      select(.metadata.name==$name and .metadata.namespace==$ns and .metadata.deletionTimestamp==null and
        (.metadata.uid | test($pattern)) and (.metadata.resourceVersion | length>0) and
        (.metadata.ownerReferences | length==1 and any(.[];
          .apiVersion=="k0smotron.io/v1beta2" and .kind=="Cluster" and .name==$cluster and .uid==$uid and .controller==true)) and
        (.data[$field] | type=="string" and length>0)) |
      {uid:.metadata.uid,resource_version:.metadata.resourceVersion,value:.data[$field]}' 2>/dev/null
}

source_identity || fail 'The original source namespace, Cluster or StatefulSet changed.'
controller_before=$(controller_identity) || fail 'The original controller is not ready.'
service_before=$(service_identity) || fail 'The original private Service changed.'
pod_ip=$(jq -er .ip <<<"$controller_before")
endpoints_match "$pod_ip" || fail 'The original private Service has no exact ready controller endpoint.'
source_secret "$cluster-kubeconfig" value >"$work/kubeconfig-source" ||
  fail 'The source Cluster does not own the expected child kubeconfig.'
source_secret "$cluster-ca" tls.crt >"$work/ca-source" ||
  fail 'The source Cluster does not own the expected CA certificate.'
jq -er .value "$work/kubeconfig-source" | base64 -d >"$work/original.kubeconfig" 2>/dev/null ||
  fail 'The original kubeconfig cannot be decoded.'
[[ -s "$work/original.kubeconfig" ]] || fail 'The original kubeconfig is empty.'

if ! kc --kubeconfig="$work/original.kubeconfig" config view --raw -o json 2>/dev/null |
  jq -er --arg endpoint "$endpoint" '
    select(.apiVersion=="v1" and .kind=="Config" and (.clusters | length==1) and (.contexts | length==1) and (.users | length==1) and
      (."current-context" | type=="string" and length>0) and .contexts[0].name==."current-context" and
      .contexts[0].context.cluster==.clusters[0].name and .contexts[0].context.user==.users[0].name and
      (.clusters[0].cluster | keys==["certificate-authority-data","server"] and .server==$endpoint and
        (."certificate-authority-data" | type=="string" and length>0)) and
      (.users[0].user | keys==["client-certificate-data","client-key-data"] and
        ([."client-certificate-data",."client-key-data"] | all(.[]; type=="string" and length>0)))) |
    .clusters[0].cluster["certificate-authority-data"]' 2>/dev/null |
  base64 -d >"$work/embedded-ca" 2>/dev/null; then
  fail 'The original kubeconfig does not use embedded TLS credentials for the approved Service.'
fi
jq -er .value "$work/ca-source" | base64 -d >"$work/source-ca" 2>/dev/null ||
  fail 'The original CA certificate cannot be decoded.'
[[ $(file_hash "$work/embedded-ca") == "$(file_hash "$work/source-ca")" ]] ||
  fail 'The original kubeconfig does not trust the source CA certificate.'
openssl x509 -in "$work/source-ca" -outform DER >"$work/source-ca.der" 2>/dev/null ||
  fail 'The source CA is not a valid certificate.'

start_probe || fail 'The isolated API probe could not start.'
probe_before=$(probe_identity) || fail 'The isolated API probe does not match the original controller image and approved client shape.'
if ! [[ $(controller_identity) == "$controller_before" && $(service_identity) == "$service_before" ]] || ! endpoints_match "$pod_ip"; then
  fail 'The source route or controller changed before the marker check.'
fi
child_kubectl=(timeout 25s kubectl --request-timeout=20s -n "$namespace" exec -i "$probe_name" -c client --
  env HTTPS_PROXY= https_proxy= HTTP_PROXY= http_proxy= ALL_PROXY= all_proxy= \
  k0s kubectl --kubeconfig=/dev/stdin --request-timeout=15s)
if ! "${child_kubectl[@]}" -n default get configmap "$marker" -o json \
  <"$work/original.kubeconfig" 2>/dev/null |
  jq -ce --slurpfile source "$approved" '
    $source[0].marker as $m |
    select(.apiVersion=="v1" and .kind=="ConfigMap" and .metadata.namespace=="default" and .metadata.name==$m.name and
      .metadata.uid==$m.uid and .metadata.resourceVersion==$m.revision and .metadata.deletionTimestamp==null and
      .data=={state:"before-handoff"}) |
    {name:.metadata.name,uid:.metadata.uid,revision:.metadata.resourceVersion,state:.data.state}' >"$work/marker" 2>/dev/null; then
  fail 'The original TLS Service did not return the approved marker.'
fi

endpoint_configmap_uid=
deadline=$((SECONDS + 60))
while (( SECONDS < deadline )); do
  if endpoint_configmap_uid=$("${child_kubectl[@]}" get --raw /api/v1/namespaces/kube-system/configmaps/control-plane-endpoint \
    <"$work/original.kubeconfig" 2>/dev/null | jq -er --slurpfile source "$approved" --arg uid "$uid_pattern" '
      select(.apiVersion=="v1" and .kind=="ConfigMap" and .metadata.namespace=="kube-system" and
        .metadata.name=="control-plane-endpoint" and .metadata.deletionTimestamp==null and (.binaryData // {})=={} and
        .data=={apiServerHost:$source[0].api_service.address,apiServerPort:"6443"}) |
      .metadata.uid | select(test($uid) and .!="00000000-0000-0000-0000-000000000000")' 2>/dev/null); then
    break
  fi
  endpoint_configmap_uid=
  sleep 2
done
[[ $endpoint_configmap_uid =~ $uid_pattern ]] || fail 'The original TLS Service did not return the expected control-plane endpoint ConfigMap.'

if ! [[ $(kc get namespace kube-system -o jsonpath='{.metadata.uid}') == "$guest_uid" &&
  $(controller_identity) == "$controller_before" && $(service_identity) == "$service_before" && $(probe_identity) == "$probe_before" ]] ||
  ! source_identity || ! endpoints_match "$pod_ip"; then
  fail 'The source route or controller changed during the marker check.'
fi
remove_probe || fail 'The isolated API probe could not be removed.'
source_secret "$cluster-kubeconfig" value >"$work/kubeconfig-current" &&
  source_secret "$cluster-ca" tls.crt >"$work/ca-current" &&
  [[ $(file_hash "$work/kubeconfig-current") == "$(file_hash "$work/kubeconfig-source")" &&
    $(file_hash "$work/ca-current") == "$(file_hash "$work/ca-source")" ]] ||
  fail 'The original kubeconfig or its source CA changed during the marker check.'

jq -n --slurpfile source "$approved" --arg input "$source_sha" --arg config "$(file_hash "$work/original.kubeconfig")" \
  --arg ca "$(file_hash "$work/source-ca.der")" --arg marker "$(file_hash "$work/marker")" --arg endpoint_uid "$endpoint_configmap_uid" '
  $source[0] as $s |
  {version:1,guest_uid:$s.guest_uid,namespace_uid:$s.namespace_uid,source_cluster_uid:$s.cluster_uid,
   service_uid:$s.api_service.uid,controller_pod_uid:$s.controller.pod_uid,source_receipt_sha256:$input,
   kubeconfig_sha256:$config,ca_certificate_sha256:$ca,marker_sha256:$marker,endpoint_configmap_uid:$endpoint_uid}' >"$work/api-evidence.json"
ln -T -- "$work/original.kubeconfig" "$out/original.kubeconfig"
ln -T -- "$work/api-evidence.json" "$out/api-evidence.json"
file_hash "$out/api-evidence.json"
