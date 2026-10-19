registry_project=acme-images
registry_repository=probe

registry_host() {
  echo "registry.$HOST"
}

registry_alias() {
  echo "mirror.$HOST"
}

registry_pin_hosts() {
  sed -i "/[[:space:]]registry\\.${HOST//./\\.}\\([[:space:]]\\|\$\\)/d" /etc/hosts
  printf '%s %s %s\n' "$VIP" "$(registry_host)" "$(registry_alias)" >>/etc/hosts
}

harbor_admin_password() {
  kubectl -n bedrock-registry get secret harbor-admin -o jsonpath='{.data.HARBOR_ADMIN_PASSWORD}' | base64 -d
}

harbor_api() {
  local method=$1 path=$2
  shift 2
  curl -sS --max-time 60 --cacert "$BEDROCK_CA" -X "$method" -u "admin:$(harbor_admin_password)" "$@" "https://$(registry_host)/api/v2.0$path"
}

harbor_code() {
  local method=$1 path=$2
  shift 2
  harbor_api "$method" "$path" -o /dev/null -w '%{http_code}' "$@"
}

registry_login_value() {
  kubectl -n "$NS" get secret "$1-docker" -o jsonpath="{.data.$2}" | base64 -d
}

registry_token() {
  local user=$1 password=$2 scope=$3
  curl -fsS --max-time 60 --cacert "$BEDROCK_CA" -u "$user:$password" "https://$(registry_host)/service/token?service=harbor-registry&scope=$scope" | jq -r .token
}

registry_token_works() {
  local token
  token=$(registry_token "$1" "$2" "$3") || return 1
  [ -n "$token" ] && [ "$token" != null ]
}

registry_credential() {
  local name=$1 permission=$2
  kubectl -n "$NS" apply -f - <<CREDENTIAL
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: RegistryCredential
metadata:
  name: $name
spec:
  registryRef:
    name: images
  permission: $permission
CREDENTIAL
  wait_condition 300 registrycredential "$name" Ready True -n "$NS"
}

registry_upload_blob() {
  local token=$1 repository=$2 content=$3 digest location separator
  digest=sha256:$(printf '%s' "$content" | sha256sum | cut -d' ' -f1)
  location=$(curl -sS --max-time 60 --cacert "$BEDROCK_CA" -D - -o /dev/null -X POST -H "Authorization: Bearer $token" "https://$(registry_host)/v2/$repository/blobs/uploads/" | awk 'tolower($1) == "location:" {print $2}' | tr -d '\r')
  case "$location" in
  /*) location=https://$(registry_host)$location ;;
  esac
  separator='?'
  case "$location" in
  *\?*) separator='&' ;;
  esac
  curl -fsS --max-time 60 --cacert "$BEDROCK_CA" -X PUT -H "Authorization: Bearer $token" -H 'Content-Type: application/octet-stream' --data-binary "$content" "$location${separator}digest=$digest" -o /dev/null
  echo "$digest"
}

registry_push_image() {
  local token=$1 repository=$2 config=$3 layer=$4 config_digest layer_digest
  config_digest=$(registry_upload_blob "$token" "$repository" '{}')
  layer_digest=$(registry_upload_blob "$token" "$repository" "$layer")
  curl -fsS --max-time 60 --cacert "$BEDROCK_CA" -X PUT -H "Authorization: Bearer $token" -H 'Content-Type: application/vnd.oci.image.manifest.v1+json' \
    --data-binary "{\"schemaVersion\":2,\"mediaType\":\"application/vnd.oci.image.manifest.v1+json\",\"config\":{\"mediaType\":\"application/vnd.oci.image.config.v1+json\",\"digest\":\"$config_digest\",\"size\":2},\"layers\":[{\"mediaType\":\"application/vnd.oci.image.layer.v1.tar\",\"digest\":\"$layer_digest\",\"size\":${#layer}}]}" \
    "https://$(registry_host)/v2/$repository/manifests/$config" -o /dev/null
}

registry_enable() {
  kubectl patch setting registry.host --type merge -p "{\"spec\":{\"value\":\"$(registry_host)\"}}"
  kubectl patch setting registry.aliases --type merge -p "{\"spec\":{\"value\":\"$(registry_alias)\"}}"
  kubectl patch setting registry.replicas --type merge -p '{"spec":{"value":"1"}}'
  kubectl patch setting registry.db-instances --type merge -p '{"spec":{"value":"1"}}'
  kubectl patch setting database.enabled --type merge -p '{"spec":{"value":"true"}}'
  registry_pin_hosts
  kubectl patch addon cnpg --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 600 addon cnpg Ready True
  kubectl patch addon registry --type merge -p '{"spec":{"enabled":true}}'
  wait_condition 120 addon registry Preflight True
  wait_condition 600 addon registry RuntimeReady True
  wait_condition 1200 addon registry Ready True
  wait_condition 600 addon registry HarborConfigured True
  wait_jsonpath 600 "Cluster in healthy state" -n bedrock-registry get cluster.postgresql.cnpg.io harbor-db -o jsonpath='{.status.phase}'
  wait_condition 300 certificate registry-public Ready True -n bedrock-registry
  expect_equal robot\$ "$(harbor_api GET /configurations | jq -r .robot_name_prefix.value)" "robot name prefix"
  expect_equal db_auth "$(harbor_api GET /configurations | jq -r .auth_mode.value)" "Harbor authentication mode"
  expect_equal adminonly "$(harbor_api GET /configurations | jq -r .project_creation_restriction.value)" "project creation restriction"
}

registry_project_and_names() {
  kubectl -n "$NS" apply -f - <<REGISTRY
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Registry
metadata:
  name: images
spec:
  name: $registry_project
  quotaGB: 5
REGISTRY
  wait_condition 300 registry images Ready True -n "$NS"
  expect_equal "$(registry_host)" "$(kubectl -n "$NS" get registry images -o jsonpath='{.status.host}')" "registry host"
  expect_equal "$registry_project" "$(kubectl -n "$NS" get registry images -o jsonpath='{.status.projectName}')" "registry project"
  expect_equal 5368709120 "$(kubectl -n "$NS" get registry images -o jsonpath='{.status.hardBytes}')" "hard bytes"
  expect_equal 200 "$(harbor_code GET "/projects/$registry_project" -H 'X-Is-Resource-Name: true')" "Harbor project"
  expect_equal no "$(as_user dana -n bedrock-registry auth can-i get secrets)" "owner reads registry secrets"
  expect_equal no "$(as_user dana -n bedrock-registry auth can-i get pods)" "owner reads registry pods"
  expect_refused "only an administrator may use a reserved registry name" as_user alice -n acme-data apply -f - <<RESERVED
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Registry
metadata:
  name: library
RESERVED
  expect_refused "only an administrator may use a reserved registry name" as_user alice -n acme-data apply -f - <<MIRROR
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Registry
metadata:
  name: mirror-docker
MIRROR
  expect_refused "only an administrator may use the name prefix platform-" as_user alice -n acme-data apply -f - <<PLATFORM
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: RegistryCredential
metadata:
  name: platform-builder
spec:
  registryRef:
    name: images
  permission: pull
PLATFORM
  kubectl -n "$NS" apply -f - <<TAKEN
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Registry
metadata:
  name: duplicate
spec:
  name: $registry_project
TAKEN
  wait_reason 300 registry duplicate Ready NameTaken -n "$NS"
  kubectl -n "$NS" delete registry duplicate --wait=true
  expect_equal 200 "$(harbor_code GET "/projects/$registry_project" -H 'X-Is-Resource-Name: true')" "the project stays after a refused duplicate is deleted"
}

registry_login() {
  local user password token
  registry_credential ci pushPull
  expect_equal kubernetes.io/dockerconfigjson "$(kubectl -n "$NS" get secret ci-docker -o jsonpath='{.type}')" "login Secret type"
  expect_equal ci-docker "$(kubectl -n "$NS" get registrycredential ci -o jsonpath='{.status.secretName}')" "credential secret name"
  user=$(registry_login_value ci username)
  password=$(registry_login_value ci password)
  expect_equal "$user" "$(kubectl -n "$NS" get registrycredential ci -o jsonpath='{.status.username}')" "credential username"
  expect_equal "$(registry_host)" "$(registry_login_value ci host)" "login host"
  retry_until 120 registry_token_works "$user" "$password" "repository:$registry_project/$registry_repository:push,pull"
  token=$(registry_token "$user" "$password" "repository:$registry_project/$registry_repository:push,pull")
  registry_push_image "$token" "$registry_project/$registry_repository" latest e2e-layer
  expect_equal 1 "$(harbor_api GET "/projects/$registry_project/repositories" -H 'X-Is-Resource-Name: true' | jq 'length')" "repositories after a push"
  expect_contains "$(curl -sS --max-time 60 --cacert "$BEDROCK_CA" -I "https://$(registry_alias)/v2/" | tr -d '\r')" "https://$(registry_alias)/service/token" "alias realm"
}

registry_spikes() {
  local user password old new manage_user manage_password code
  user=$(registry_login_value ci username)
  old=$(registry_login_value ci password)
  kubectl -n "$NS" delete secret ci-docker
  retry_until 180 kubectl -n "$NS" get secret ci-docker
  new=$(registry_login_value ci password)
  test "$old" != "$new"
  retry_until 120 registry_token_works "$user" "$new" "repository:$registry_project/$registry_repository:pull"
  if registry_token_works "$user" "$old" "repository:$registry_project/$registry_repository:pull"; then
    echo "the old robot secret still works" >&2
    return 1
  fi
  echo "spike robot-secret-patch: PATCH /robots/{id} with a new secret works on this Harbor"
  registry_credential maintainer manage
  manage_user=$(registry_login_value maintainer username)
  manage_password=$(registry_login_value maintainer password)
  code=$(curl -sS --max-time 60 --cacert "$BEDROCK_CA" -o /dev/null -w '%{http_code}' -u "$manage_user:$manage_password" -H 'X-Is-Resource-Name: true' "https://$(registry_host)/api/v2.0/projects/$registry_project/repositories")
  expect_equal 200 "$code" "manage robot lists repositories"
  code=$(curl -sS --max-time 60 --cacert "$BEDROCK_CA" -o /dev/null -w '%{http_code}' -X DELETE -u "$manage_user:$manage_password" -H 'X-Is-Resource-Name: true' "https://$(registry_host)/api/v2.0/projects/$registry_project/repositories/$registry_repository")
  expect_equal 200 "$code" "manage robot deletes a repository"
  expect_equal 0 "$(harbor_api GET "/projects/$registry_project/repositories" -H 'X-Is-Resource-Name: true' | jq 'length')" "repositories after the delete"
  echo "spike manage-robot: a manage robot lists and deletes a repository"
  kubectl -n "$NS" delete registrycredential maintainer
  wait_gone 300 secret maintainer-docker -n "$NS"
}

registry_quota_and_retention() {
  kubectl -n "$NS" patch registry images --type merge -p '{"spec":{"quotaGB":1,"retention":{"keepLastN":3,"schedule":"daily"}}}'
  wait_jsonpath 300 1073741824 -n "$NS" get registry images -o jsonpath='{.status.hardBytes}'
  retry_until 300 registry_has_retention
  kubectl -n "$NS" patch registry images --type merge -p '{"spec":{"retention":null}}'
  retry_until 300 registry_retention_cleared
}

registry_has_retention() {
  local id
  id=$(harbor_api GET "/projects/$registry_project" -H 'X-Is-Resource-Name: true' | jq -r '.metadata.retention_id // empty')
  [ -n "$id" ] && [ "$(harbor_api GET "/retentions/$id" | jq '.rules | length')" = 1 ]
}

registry_retention_cleared() {
  local id
  id=$(harbor_api GET "/projects/$registry_project" -H 'X-Is-Resource-Name: true' | jq -r '.metadata.retention_id // empty')
  [ -n "$id" ] && [ "$(harbor_api GET "/retentions/$id" | jq '.rules | length')" = 0 ]
}

registry_force_destroy() {
  local token
  kubectl -n "$NS" delete registrycredential ci --wait=true
  wait_gone 300 secret ci-docker -n "$NS"
  registry_credential pusher pushPull
  token=$(registry_token "$(registry_login_value pusher username)" "$(registry_login_value pusher password)" "repository:$registry_project/$registry_repository:push,pull")
  registry_push_image "$token" "$registry_project/$registry_repository" latest e2e-layer
  kubectl -n "$NS" delete registry images --wait=false
  wait_reason 300 registry images Ready NotEmpty -n "$NS"
  kubectl -n "$NS" patch registry images --type merge -p '{"spec":{"forceDestroy":true}}'
  wait_gone 600 registry images -n "$NS"
  expect_equal 404 "$(harbor_code GET "/projects/$registry_project" -H 'X-Is-Resource-Name: true')" "Harbor project after the delete"
  kubectl -n "$NS" delete registrycredential pusher --wait=true
  wait_gone 300 secret pusher-docker -n "$NS"
}

registry_disable() {
  kubectl patch addon registry --type merge -p '{"spec":{"enabled":false}}'
  wait_reason 600 addon registry Ready Disabled
  expect_refused NotFound kubectl -n bedrock-registry get deployment harbor-core
  kubectl delete namespace bedrock-registry --wait=false
  wait_gone 900 namespace bedrock-registry
}
