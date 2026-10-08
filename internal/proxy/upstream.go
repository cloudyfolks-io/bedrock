package proxy

import (
	"fmt"
	"net/http"
	"net/url"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
)

const proxyUserAgent = "bedrock-proxy"

type Upstream struct {
	target    *url.URL
	transport http.RoundTripper
}

func NewUpstream(cfg *rest.Config) (*Upstream, error) {
	bare := &rest.Config{
		Host:    cfg.Host,
		APIPath: cfg.APIPath,
		TLSClientConfig: rest.TLSClientConfig{
			Insecure:   cfg.TLSClientConfig.Insecure,
			ServerName: cfg.TLSClientConfig.ServerName,
			CAFile:     cfg.TLSClientConfig.CAFile,
			CAData:     cfg.TLSClientConfig.CAData,
			NextProtos: cfg.TLSClientConfig.NextProtos,
		},
		Proxy:   cfg.Proxy,
		Dial:    cfg.Dial,
		QPS:     -1,
		Timeout: 0,
	}
	target, _, err := rest.DefaultServerURL(cfg.Host, cfg.APIPath, schema.GroupVersion{}, rest.IsConfigTransportTLS(*bare))
	if err != nil {
		return nil, fmt.Errorf("parse the API server address: %w", err)
	}
	transport, err := rest.TransportFor(bare)
	if err != nil {
		return nil, fmt.Errorf("build the API server transport: %w", err)
	}
	return &Upstream{target: target, transport: transport}, nil
}

func (u *Upstream) RESTConfig(token string) *rest.Config {
	return &rest.Config{
		Host:        u.target.String(),
		Transport:   u.transport,
		BearerToken: token,
		UserAgent:   proxyUserAgent,
		QPS:         -1,
		ContentConfig: rest.ContentConfig{
			ContentType:        runtime.ContentTypeJSON,
			AcceptContentTypes: runtime.ContentTypeJSON,
		},
	}
}

func (u *Upstream) Target() *url.URL {
	target := *u.target
	return &target
}

func (u *Upstream) RoundTripper() http.RoundTripper {
	return u.transport
}
