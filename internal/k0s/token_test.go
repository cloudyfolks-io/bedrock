package k0s

import "testing"

func TestTokenRoundTrip(t *testing.T) {
	in := Token{Version: "v0.3.0", Roles: []string{"control-plane", "workload"}, K0sToken: "abc", K0sConfig: []byte("apiVersion: k0s"), VIP: "10.0.10.10", Image: "ghcr.io/cloudyfolks-io/bedrock:v0.3.0", K0sVersion: "v1.36.3+k0s.0", K0sChecksums: map[string]string{"amd64": "sha256:aa"}, SupportedOS: []string{"ubuntu-24.04"}}
	encoded, err := EncodeToken(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeToken(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if out.VIP != in.VIP || string(out.K0sConfig) != "apiVersion: k0s" || len(out.Roles) != 2 || out.K0sChecksums["amd64"] != "sha256:aa" {
		t.Fatalf("round trip %+v", out)
	}
	if _, err := DecodeToken("not base64!"); err == nil {
		t.Fatal("garbage must fail")
	}
}

func TestTokenCarriesAuthnFiles(t *testing.T) {
	in := Token{Version: "v0.3.0", Roles: []string{"control-plane"}, K0sToken: "abc", AuthnFiles: map[string][]byte{"authentication.yaml": []byte("a"), "webhook.kubeconfig": []byte("w")}}
	encoded, err := EncodeToken(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := DecodeToken(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(out.AuthnFiles["authentication.yaml"]) != "a" || string(out.AuthnFiles["webhook.kubeconfig"]) != "w" {
		t.Fatalf("authn files %v", out.AuthnFiles)
	}
	worker, err := EncodeToken(Token{Version: "v0.3.0", Roles: []string{"workload"}, K0sToken: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := DecodeToken(worker); err != nil || decoded.AuthnFiles != nil {
		t.Fatalf("a worker token has no authn files: %v %v", decoded.AuthnFiles, err)
	}
}
