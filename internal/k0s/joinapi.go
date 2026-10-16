package k0s

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	caPath            = "/v1beta1/ca"
	maxErrorBodyBytes = 200
)

type CA struct {
	Cert []byte
	Key  []byte
}

type caResponse struct {
	Key  []byte `json:"key"`
	Cert []byte `json:"cert"`
}

func FetchCA(ctx context.Context, k0sToken string) (CA, error) {
	kubeconfig, err := decodeK0sToken(k0sToken)
	if err != nil {
		return CA{}, fmt.Errorf("decode k0s token: %w", err)
	}
	restConfig, err := clientcmd.RESTConfigFromKubeConfig(kubeconfig)
	if err != nil {
		return CA{}, fmt.Errorf("read k0s token kubeconfig: %w", err)
	}
	httpClient, err := rest.HTTPClientFor(restConfig)
	if err != nil {
		return CA{}, fmt.Errorf("build k0s join api client: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, restConfig.Host+caPath, nil)
	if err != nil {
		return CA{}, err
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return CA{}, fmt.Errorf("call k0s join api: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, maxErrorBodyBytes))
		return CA{}, fmt.Errorf("k0s join api answered %d: %s", response.StatusCode, body)
	}
	var answer caResponse
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return CA{}, fmt.Errorf("parse k0s join api answer: %w", err)
	}
	return CA{Cert: answer.Cert, Key: answer.Key}, nil
}

func decodeK0sToken(token string) ([]byte, error) {
	zipped, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	reader, err := gzip.NewReader(bytes.NewReader(zipped))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(reader)
}
