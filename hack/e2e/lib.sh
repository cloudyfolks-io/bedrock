pin_hosts() {
  local vip=$1 host=$2 escaped
  escaped=${host//./\\.}
  sed -i "/[[:space:]]sso\\.$escaped\\([[:space:]]\\|\$\\)/d" /etc/hosts
  printf '%s %s sso.%s api.%s console.%s upload.%s\n' "$vip" "$host" "$host" "$host" "$host" "$host" >>/etc/hosts
}

dump_api_group() {
  local group=$1 resource
  for resource in $(kubectl api-resources --api-group="$group" --verbs=list -o name 2>/dev/null); do
    echo "--- $resource"
    kubectl get "$resource" -A -o yaml || true
  done
}

dump_logs() {
  local namespace=$1 selector=$2
  kubectl -n "$namespace" logs -l "$selector" --tail=200 --all-containers --prefix || true
}

retry_until() {
  local timeout=$1 deadline
  shift
  deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if [ "$SECONDS" -ge "$deadline" ]; then
      echo "timeout after ${timeout}s: $*" >&2
      return 1
    fi
    sleep 3
  done
}

jsonpath_of() {
  kubectl "$@" 2>/dev/null
}

jsonpath_equals() {
  local want=$1
  shift
  [ "$(jsonpath_of "$@")" = "$want" ]
}

jsonpath_not_empty() {
  [ -n "$(jsonpath_of "$@")" ]
}

wait_jsonpath() {
  local timeout=$1 want=$2
  shift 2
  retry_until "$timeout" jsonpath_equals "$want" "$@" || {
    echo "want '$want' from: kubectl $*; got '$(jsonpath_of "$@")'" >&2
    return 1
  }
}

wait_not_empty() {
  local timeout=$1
  shift
  retry_until "$timeout" jsonpath_not_empty "$@" || {
    echo "empty result from: kubectl $*" >&2
    return 1
  }
}

wait_gone() {
  local timeout=$1
  shift
  retry_until "$timeout" kubectl_absent "$@" || {
    echo "still present: kubectl $*" >&2
    return 1
  }
}

list_empty() {
  [ -z "$(kubectl "$@" -o name 2>/dev/null)" ]
}

wait_empty() {
  local timeout=$1
  shift
  retry_until "$timeout" list_empty "$@" || {
    echo "objects remain: kubectl $*" >&2
    kubectl "$@" -o name >&2 || true
    return 1
  }
}

kubectl_absent() {
  ! kubectl get "$@" >/dev/null 2>&1
}

condition_status() {
  local kind=$1 name=$2 type=$3
  shift 3
  kubectl "$@" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$type\")].status}" 2>/dev/null
}

condition_reason() {
  local kind=$1 name=$2 type=$3
  shift 3
  kubectl "$@" get "$kind" "$name" -o jsonpath="{.status.conditions[?(@.type==\"$type\")].reason}" 2>/dev/null
}

wait_condition() {
  local timeout=$1 kind=$2 name=$3 type=$4 status=$5
  shift 5
  retry_until "$timeout" condition_is "$status" "$kind" "$name" "$type" "$@" || {
    echo "condition $type of $kind/$name is '$(condition_status "$kind" "$name" "$type" "$@")', want $status" >&2
    kubectl "$@" get "$kind" "$name" -o jsonpath='{.status.conditions}' >&2 || true
    return 1
  }
}

condition_is() {
  local status=$1
  shift
  [ "$(condition_status "$@")" = "$status" ]
}

wait_reason() {
  local timeout=$1 kind=$2 name=$3 type=$4 reason=$5
  shift 5
  retry_until "$timeout" reason_is "$reason" "$kind" "$name" "$type" "$@" || {
    echo "reason of $type on $kind/$name is '$(condition_reason "$kind" "$name" "$type" "$@")', want $reason" >&2
    return 1
  }
}

reason_is() {
  local reason=$1
  shift
  [ "$(condition_reason "$@")" = "$reason" ]
}

expect_equal() {
  local want=$1 got=$2 what=$3
  if [ "$want" != "$got" ]; then
    echo "$what: want '$want', got '$got'" >&2
    return 1
  fi
}

expect_contains() {
  local text=$1 needle=$2 what=$3
  case "$text" in
  *"$needle"*) ;;
  *)
    echo "$what: '$needle' not found in: $text" >&2
    return 1
    ;;
  esac
}

expect_refused() {
  local needle=$1 out
  shift
  if out=$("$@" 2>&1); then
    echo "expected a refusal containing '$needle': $*" >&2
    return 1
  fi
  expect_contains "$out" "$needle" "refusal of $*"
}

as_user() {
  local user=$1
  shift
  kubectl --as="bedrock:$user" "$@"
}
