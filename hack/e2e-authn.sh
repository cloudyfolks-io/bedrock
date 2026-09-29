#!/usr/bin/env bash
set -euo pipefail

admin_password_file=${ADMIN_PASSWORD_FILE:-/var/lib/bedrock/e2e-admin-password}
bin=${BEDROCK_BIN:-bin/bedrock}
test_bin=${E2E_TEST:-dist/e2e-authn.test}
workdir=$(mktemp -d)
export KUBECONFIG=/var/lib/k0s/pki/admin.conf
export BEDROCK_CA=/var/lib/bedrock/authn/ca.crt

dump() {
  local status=$?
  if [ "$status" -ne 0 ]; then
    echo "--- authn deployment"
    kubectl -n bedrock-system get deploy/bedrock-authn -o wide || true
    echo "--- authn log"
    kubectl -n bedrock-system logs deploy/bedrock-authn --tail=300 --all-containers || true
    echo "--- operator log"
    kubectl -n bedrock-system logs deploy/bedrock-operator --tail=300 || true
    echo "--- openldap and dex"
    kubectl -n authn-e2e get pods -o wide || true
    kubectl -n authn-e2e logs deploy/openldap --tail=100 || true
    kubectl -n authn-e2e logs deploy/dex --tail=100 || true
    echo "--- auth requests"
    kubectl -n bedrock-system get authrequests -o yaml || true
    echo "--- device requests"
    kubectl -n bedrock-system get devicerequests -o yaml || true
    echo "--- kube-apiserver authentication lines"
    journalctl -u k0scontroller --no-pager | grep -i authentication | tail -100 || true
  fi
  rm -rf "$workdir"
}
trap dump EXIT

test -f "$admin_password_file"
test -x "$test_bin"
vip=$(kubectl get cluster cluster -o jsonpath='{.spec.api.vip}')
host=$(echo "$vip" | tr '.' '-').sslip.io

k0s ctr --namespace k8s.io images import dist/cache/e2e-authn/openldap.tar
k0s ctr --namespace k8s.io images import dist/cache/e2e-authn/dex.tar

kubectl apply -f test/e2e/authn/fixtures/openldap.yaml
BEDROCK_HOST=$host envsubst '${BEDROCK_HOST}' <test/e2e/authn/fixtures/dex.yaml | kubectl apply -f -

for _ in $(seq 1 30); do
  ca_bundle_b64=$(kubectl get secret -n cert-manager bedrock-ca -o jsonpath='{.data.ca\.crt}' 2>/dev/null || true)
  if [ -n "$ca_bundle_b64" ]; then
    break
  fi
  sleep 5
done
test -n "$ca_bundle_b64"
ca_bundle=$(echo "$ca_bundle_b64" | base64 -d | awk '{printf "%s\\n", $0}')
BEDROCK_HOST=$host LDAP_CA_BUNDLE=$ca_bundle DEX_CA_BUNDLE=$ca_bundle \
  envsubst '${BEDROCK_HOST} ${LDAP_CA_BUNDLE} ${DEX_CA_BUNDLE}' <test/e2e/authn/fixtures/providers.yaml.tmpl | kubectl apply -f -

kubectl -n authn-e2e rollout status deployment/openldap --timeout=180s
kubectl -n authn-e2e rollout status deployment/dex --timeout=180s
kubectl -n bedrock-system rollout status deployment/bedrock-authn --timeout=180s

for _ in $(seq 1 30); do
  if kubectl run ldap-check --rm -i --restart=Never --image=docker.io/osixia/openldap:1.5.0 --env=LDAPTLS_REQCERT=never --command -- \
    ldapsearch -H ldaps://openldap.authn-e2e.svc:636 -D cn=reader,dc=bedrock,dc=test -w reader-e2e-pw \
    -b ou=people,dc=bedrock,dc=test "(uid=alice)" -o ldif-wrap=no >"$workdir/ldapsearch.out" 2>"$workdir/ldapsearch.err"; then
    break
  fi
  sleep 5
done
grep -q '^dn: uid=alice,ou=people,dc=bedrock,dc=test$' "$workdir/ldapsearch.out"

for _ in $(seq 1 30); do
  if curl -sk -m 10 "https://dex.$host/.well-known/openid-configuration" >"$workdir/dex.out" 2>"$workdir/dex.err"; then
    break
  fi
  sleep 5
done
grep -q '"issuer"' "$workdir/dex.out"

install -m 0755 "$bin" /usr/local/bin/bedrock

BEDROCK_HOST=$host BEDROCK_BIN=/usr/local/bin/bedrock ADMIN_PASSWORD=$(cat "$admin_password_file") \
  "$test_bin" -test.count=1 -test.v -test.timeout 20m
echo "e2e-authn passed"
