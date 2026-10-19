rotation_namespace=lab-rotation
rotation_secret_count=2000

rotation_supported() {
  kubectl get crd clusters.bedrock.cloudyfolks.io -o json |
    jq -e '[.spec.versions[].schema.openAPIV3Schema.properties.spec.properties.encryption.properties | has("action")] | any' >/dev/null
}

rotation_phase() {
  kubectl get cluster cluster -o jsonpath='{.status.encryption.phase}'
}

rotation_rotated_epoch() {
  local rotated
  rotated=$(kubectl get cluster cluster -o jsonpath='{.status.encryption.lastRotatedAt}')
  if [ -n "$rotated" ]; then
    date -u -d "$rotated" +%s
  else
    echo 0
  fi
}

rotation_started() {
  [ -n "$(rotation_phase)" ]
}

rotation_done_since() {
  [ -z "$(rotation_phase)" ] && [ "$(rotation_rotated_epoch)" -ge "$1" ]
}

secret_key_name() {
  etcd_value "/registry/secrets/$1/$2" | LC_ALL=C tr -d '\000' | LC_ALL=C awk -F: 'NR == 1 { print $5 }'
}

sampled_secrets() {
  echo "$rotation_namespace/lab-rot-1"
  echo "$rotation_namespace/lab-rot-$rotation_secret_count"
  kubectl get secrets -A --no-headers -o custom-columns=NS:.metadata.namespace,NAME:.metadata.name |
    awk 'NR % 10 == 1 || ($1 == "kube-system" && $2 == "bedrock-secrets-encryption") { print $1 "/" $2 }'
}

sampled_key_names() {
  local namespace name
  while IFS=/ read -r namespace name; do
    secret_key_name "$namespace" "$name"
  done < <(sampled_secrets)
}

key_rotation_fixture() {
  local padding
  padding=$(head -c 2048 /dev/zero | tr '\0' x)
  kubectl create namespace "$rotation_namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  seq 1 "$rotation_secret_count" |
    awk -v namespace="$rotation_namespace" -v padding="$padding" '{ printf "apiVersion: v1\nkind: Secret\nmetadata:\n  name: lab-rot-%d\n  namespace: %s\nstringData:\n  payload: \"%s\"\n---\n", $1, namespace, padding }' |
    kubectl apply -f - >/dev/null
  secret_key_name "$rotation_namespace" lab-rot-1
}

pre_rotation_archive() {
  local archive
  archive=$(find "$etcd_backups" -maxdepth 1 -name '*.tar.gz.age' | sort | tail -n 1)
  if [ -z "$archive" ]; then
    archive=$(take_etcd_snapshot "$(date -u +%Y%m%dT%H%M%SZ)")
  fi
  echo "$archive"
}

archive_digest() {
  "$bedrock_bin" backup decrypt --in "$1" --out - | sha256sum | cut -d' ' -f1
}

start_key_rotation() {
  kubectl patch cluster cluster --type merge -p '{"spec":{"encryption":{"action":"rotate"}}}' >/dev/null
  retry_until 300 rotation_started
}

wait_rotation_phase() {
  local deadline=$((SECONDS + $1)) phase=$2 started=$3
  until [ "$(rotation_phase)" = "$phase" ]; do
    if rotation_done_since "$started"; then
      return 3
    fi
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "phase $phase not reached, status: $(kubectl get cluster cluster -o jsonpath='{.status.encryption}')" >&2
      return 1
    fi
    sleep 1
  done
}

wait_rotation_done() {
  retry_until "$1" rotation_done_since "$2" || {
    kubectl get cluster cluster -o jsonpath='{.status.encryption}' >&2
    return 1
  }
}

verify_rotated_secrets() {
  local old=$1 keys distinct count
  keys=$(sampled_key_names)
  distinct=$(printf '%s\n' "$keys" | sort -u)
  count=$(printf '%s\n' "$keys" | wc -l)
  test "$count" -ge 50
  expect_equal 1 "$(printf '%s\n' "$distinct" | wc -l)" "number of key names among $count sampled Secrets: $(printf '%s' "$distinct" | tr '\n' ' ')"
  test -n "$distinct"
  if [ "$distinct" = "$old" ]; then
    echo "the sampled Secrets still use the old key $old" >&2
    return 1
  fi
  echo "info: key rotation: $count sampled Secrets moved from $old to $distinct, the operator rewrote $(kubectl get cluster cluster -o jsonpath='{.status.encryption.rewrittenSecrets}') Secrets"
}

assert_old_key_gone() {
  local file=/etc/bedrock/encryption/encryption.yaml
  if grep -Eq "name: $1\$" "$file"; then
    echo "$file still holds the key $1" >&2
    return 1
  fi
  expect_equal 1 "$(grep -Ec 'name: key[0-9]+$' "$file")" "keys in $file"
}

rotate_host_key_and_decrypt() {
  local archive=$1 digest=$2
  "$bedrock_bin" backup rotate-key >/dev/null
  test "$(find /etc/bedrock/backup/retired -type f | wc -l)" -ge 1
  expect_equal "$digest" "$(archive_digest "$archive")" "digest of the archive made before the host key rotation"
  echo "info: key rotation: the archive $(basename "$archive"), made before the rotation, still decrypts after backup rotate-key"
}

key_rotation_cleanup() {
  kubectl delete namespace "$rotation_namespace" --wait=false
  wait_gone 900 namespace "$rotation_namespace"
}
