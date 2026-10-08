cirros_reference() {
  grep '^quay.io/kubevirt/cirros-container-disk-demo:v1.9.0@sha256:' dist/release/images.txt
}

images_cluster_image() {
  local reference
  reference=$(cirros_reference)
  test -n "$reference"
  kubectl apply -f - <<IMAGE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: ClusterImage
metadata:
  name: cirros
spec:
  sizeGi: 1
  source:
    registry:
      image: $reference
IMAGE
  wait_jsonpath 900 Ready get clusterimage cirros -o jsonpath='{.status.phase}'
  expect_equal cirros "$(kubectl get clusterimage cirros -o jsonpath='{.status.dataSource}')" "cirros data source"
  kubectl -n bedrock-images get datasource cirros
  expect_equal 1073741824 "$(kubectl get clusterimage cirros -o jsonpath='{.status.sizeBytes}')" "cirros size"
  expect_equal True "$(condition_status clusterimage cirros Ready)" "cirros Ready condition"
  wait_not_empty 60 -n bedrock-images get datasource cirros -o jsonpath='{.spec.source.snapshot.name}'
  wait_condition 300 cluster cluster UploadReady True
  expect_equal 'Host(`upload.'"$HOST"'`) && PathPrefix(`/v1beta1/upload`)' "$(kubectl -n cdi get ingressroute bedrock-upload -o jsonpath='{.spec.routes[0].match}')" "upload route"
  expect_equal snapshot "$(kubectl get storageprofile block -o jsonpath='{.status.dataImportCronSourceFormat}')" "block storage profile source format"
}

upload_request() {
  local method=$1 token=$2
  curl -s -o /dev/null -w '%{http_code}' -X "$method" -H "Authorization: Bearer $token" --cacert "$BEDROCK_CA" \
    "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/$NS/images/upload-disk/upload"
}

images_upload() {
  local token response url upload_token started
  token=$(cat "$TOKEN_DIR/dana")
  truncate -s 80M "$workdir/disk.raw"
  kubectl -n "$NS" apply -f - <<IMAGE
apiVersion: bedrock.cloudyfolks.io/v1alpha1
kind: Image
metadata:
  name: upload-disk
spec:
  sizeGi: 1
  source:
    upload: {}
IMAGE
  wait_jsonpath 600 WaitingForUpload -n "$NS" get image upload-disk -o jsonpath='{.status.phase}'
  expect_equal "https://upload.$HOST/v1beta1/upload-async" "$(kubectl -n "$NS" get image upload-disk -o jsonpath='{.status.uploadURL}')" "upload url"
  expect_equal 405 "$(upload_request GET "$token")" "GET on the upload subresource"
  response=$(curl -sf -X POST -H "Authorization: Bearer $token" --cacert "$BEDROCK_CA" \
    "https://api.$HOST/apis/bedrock.cloudyfolks.io/v1alpha1/namespaces/$NS/images/upload-disk/upload")
  url=$(echo "$response" | jq -r .url)
  upload_token=$(echo "$response" | jq -r .token)
  expect_equal "https://upload.$HOST/v1beta1/upload-async" "$url" "upload url from the proxy"
  test -n "$upload_token"
  expect_equal 401 "$(curl -s -o /dev/null -w '%{http_code}' -X POST -H 'Authorization: Bearer garbage' --cacert "$BEDROCK_CA" --data-binary @"$workdir/disk.raw" "$url")" "upload with a bad token"
  started=$SECONDS
  curl -sf --limit-rate 1M --cacert "$BEDROCK_CA" -H "Authorization: Bearer $upload_token" --data-binary @"$workdir/disk.raw" "$url"
  test $((SECONDS - started)) -ge 61
  wait_jsonpath 600 Ready -n "$NS" get image upload-disk -o jsonpath='{.status.phase}'
  expect_equal 409 "$(upload_request POST "$token")" "second upload"
  expect_contains "$(curl -si -X OPTIONS -H "Origin: https://console.$HOST" -H 'Access-Control-Request-Method: POST' --cacert "$BEDROCK_CA" "$url")" "access-control-allow-origin: https://console.$HOST" "upload CORS"
}
