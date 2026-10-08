package compute

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func VNC(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		serveConsole(d, ctx, c, key, w, r, "vnc")
	}
}

func Serial(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		serveConsole(d, ctx, c, key, w, r, "console")
	}
}

func serveConsole(d Deps, ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request, upstream string) {
	if err := checkConsoleRequest(r); err != nil {
		writeError(w, err)
		return
	}
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		writeError(w, err)
		return
	}
	if vm.Status.Phase != v1alpha1.VMPhaseRunning && vm.Status.Phase != v1alpha1.VMPhaseMigrating {
		writeError(w, conflict("the virtual machine is %s and must be Running", orUnknown(vm.Status.Phase)))
		return
	}
	target, err := consoleTarget(d.Config, key, upstream)
	if err != nil {
		writeError(w, internalError(err))
		return
	}
	transport, err := rest.TransportFor(rest.CopyConfig(d.Config))
	if err != nil {
		writeError(w, internalError(err))
		return
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.Out.Header.Del("Authorization")
			pr.Out.Header.Del("Cookie")
		},
		FlushInterval: -1,
		Transport:     transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, ActionError{Code: http.StatusBadGateway, Reason: metav1.StatusReasonServiceUnavailable, Message: "the console is not reachable: " + err.Error()})
		},
	}
	proxy.ServeHTTP(w, r)
}

func checkConsoleRequest(r *http.Request) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return badRequest("the console needs a WebSocket upgrade")
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host {
		return ActionError{Code: http.StatusForbidden, Reason: metav1.StatusReasonForbidden, Message: "the origin of the request does not match its host"}
	}
	return nil
}

func consoleTarget(cfg *rest.Config, key client.ObjectKey, upstream string) (*url.URL, error) {
	return kubeVirtURL(cfg, "apis", "subresources.kubevirt.io", "v1", "namespaces", key.Namespace, "virtualmachineinstances", key.Name, upstream)
}
