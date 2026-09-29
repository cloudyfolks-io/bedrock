package cliauth

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientauthenticationv1 "k8s.io/client-go/pkg/apis/clientauthentication/v1"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

const contextName = "bedrock"

func Kubeconfig(server string, ca []byte, issuer, caFile string) ([]byte, error) {
	config := clientcmdapi.NewConfig()
	config.Clusters[contextName] = &clientcmdapi.Cluster{Server: server, CertificateAuthorityData: ca}
	config.AuthInfos[contextName] = &clientcmdapi.AuthInfo{Exec: &clientcmdapi.ExecConfig{
		APIVersion:      "client.authentication.k8s.io/v1",
		Command:         "bedrock",
		Args:            execArgs(issuer, caFile),
		InteractiveMode: clientcmdapi.IfAvailableExecInteractiveMode,
	}}
	config.Contexts[contextName] = &clientcmdapi.Context{Cluster: contextName, AuthInfo: contextName}
	config.CurrentContext = contextName
	return clientcmd.Write(*config)
}

func execArgs(issuer, caFile string) []string {
	if caFile == "" {
		return []string{"login", "--server", issuer, "--exec-credential"}
	}
	return []string{"login", "--server", issuer, "--ca-file", caFile, "--exec-credential"}
}

func ExecCredential(tokens Tokens) ([]byte, error) {
	credential := clientauthenticationv1.ExecCredential{
		TypeMeta: metav1.TypeMeta{Kind: "ExecCredential", APIVersion: "client.authentication.k8s.io/v1"},
		Status:   &clientauthenticationv1.ExecCredentialStatus{Token: tokens.AccessToken, ExpirationTimestamp: &metav1.Time{Time: tokens.Expiry}},
	}
	return json.Marshal(credential)
}

func ClusterInfo(ctx context.Context, c *http.Client, issuer string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, issuer+"/api/v1/cluster", nil)
	if err != nil {
		return "", nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("cluster info %s: status %d", issuer, resp.StatusCode)
	}
	var body struct {
		Server               string `json:"server"`
		CertificateAuthority string `json:"certificateAuthority"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", nil, err
	}
	return body.Server, []byte(body.CertificateAuthority), nil
}
