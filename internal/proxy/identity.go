package proxy

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/authn/httpjson"
	"github.com/cloudyfolks-io/bedrock/internal/cliauth"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	clusterCAConfigMap  = "kube-root-ca.crt"
	platformCAConfigMap = "bedrock-platform-ca"
	caKey               = "ca.crt"
	apiServerPort       = "6443"
)

type sessionUser struct {
	Name        string   `json:"name"`
	DisplayName string   `json:"displayName"`
	Email       string   `json:"email"`
	Groups      []string `json:"groups"`
}

type sessionView struct {
	User      sessionUser `json:"user"`
	Admin     bool        `json:"admin"`
	CSRF      string      `json:"csrf"`
	ExpiresAt string      `json:"expiresAt"`
	Host      string      `json:"host"`
}

func SessionHandler(host func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		caller, found := CallerFrom(r.Context())
		if !found || caller.Principal == nil {
			httpjson.WriteError(w, http.StatusUnauthorized, "unauthenticated")
			return
		}
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		principal := caller.Principal
		httpjson.Write(w, http.StatusOK, sessionView{
			User: sessionUser{
				Name:        principal.Subject,
				DisplayName: principal.DisplayName,
				Email:       principal.Email,
				Groups:      append([]string{}, principal.Groups...),
			},
			Admin:     principal.Admin,
			CSRF:      principal.CSRF,
			ExpiresAt: principal.ExpiresAt.UTC().Format(time.RFC3339),
			Host:      current,
		})
	})
}

func KubeconfigHandler(reader client.Reader, host func(context.Context) (string, error)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current, err := host(r.Context())
		if err != nil {
			hostUnavailable(w, r, err)
			return
		}
		ca, err := configMapValue(r.Context(), reader, clusterCAConfigMap)
		if err != nil || ca == "" {
			slog.ErrorContext(r.Context(), "cluster CA unavailable", "error", err)
			httpjson.WriteError(w, http.StatusServiceUnavailable, "upstream_unavailable")
			return
		}
		kubeconfig, err := cliauth.Kubeconfig("https://api."+current+":"+apiServerPort, []byte(ca), "https://sso."+current, "")
		if err != nil {
			httpjson.Internal(w, r, "render the kubeconfig", err)
			return
		}
		download(w, "application/yaml", "bedrock.kubeconfig", kubeconfig)
	})
}

func CAHandler(reader client.Reader) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ca, err := configMapValue(r.Context(), reader, platformCAConfigMap)
		if err != nil || ca == "" {
			httpjson.WriteError(w, http.StatusNotFound, "not_found")
			return
		}
		download(w, "application/x-pem-file", "bedrock-ca.crt", []byte(ca))
	})
}

func configMapValue(ctx context.Context, reader client.Reader, name string) (string, error) {
	var configMap corev1.ConfigMap
	if err := reader.Get(ctx, client.ObjectKey{Namespace: release.SystemNamespace, Name: name}, &configMap); err != nil {
		return "", err
	}
	return configMap.Data[caKey], nil
}

func download(w http.ResponseWriter, contentType, filename string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(body)
}
