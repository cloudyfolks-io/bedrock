encryption_join_fetch() {
  local token
  token=$("$bin" token create --roles control-plane --expiry 10m)
  JOIN_TOKEN=$token "$test_bin" -test.count=1 -test.v -test.timeout 5m -test.run '^TestJoinFetchesEncryption$'
}
