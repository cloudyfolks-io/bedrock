package container

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const defaultExecCommand = "/bin/sh"

type ExecQuery struct {
	Pod     string
	Command []string
	TTY     bool
}

func ParseExecQuery(values map[string][]string) (ExecQuery, error) {
	query := ExecQuery{Command: []string{defaultExecCommand}}
	if v := values["pod"]; len(v) > 0 {
		query.Pod = v[0]
	}
	if v := values["command"]; len(v) > 0 {
		for _, word := range v {
			if word == "" {
				return ExecQuery{}, badRequest("the command must not hold an empty word")
			}
		}
		query.Command = append([]string(nil), v...)
	}
	if v := values["tty"]; len(v) > 0 {
		tty, err := strconv.ParseBool(v[0])
		if err != nil {
			return ExecQuery{}, badRequest("tty must be true or false")
		}
		query.TTY = tty
	}
	return query, nil
}

func ExecTarget(cfg *rest.Config, namespace, pod string, query ExecQuery) (*url.URL, error) {
	base, _, err := rest.DefaultServerURL(cfg.Host, cfg.APIPath, schema.GroupVersion{}, rest.IsConfigTransportTLS(*cfg))
	if err != nil {
		return nil, err
	}
	target := *base
	target.Path = path.Join("/", base.Path, "api", "v1", "namespaces", namespace, "pods", pod, "exec")
	values := url.Values{}
	values.Set("container", appContainerName)
	values.Set("stdin", "true")
	values.Set("stdout", "true")
	values.Set("stderr", strconv.FormatBool(!query.TTY))
	values.Set("tty", strconv.FormatBool(query.TTY))
	for _, word := range query.Command {
		values.Add("command", word)
	}
	target.RawQuery = values.Encode()
	return &target, nil
}

var handshakeHeaderNames = []string{
	"Connection",
	"Upgrade",
	"Sec-Websocket-Key",
	"Sec-Websocket-Version",
	"Sec-Websocket-Protocol",
	"Sec-Websocket-Extensions",
}

func handshakeHeaders(in http.Header) http.Header {
	out := http.Header{}
	for _, name := range handshakeHeaderNames {
		if values := in.Values(name); len(values) > 0 {
			out[name] = append([]string(nil), values...)
		}
	}
	return out
}

func checkExecRequest(r *http.Request) error {
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		return badRequest("exec needs a WebSocket upgrade")
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host != r.Host {
		return forbidden("the origin of the request does not match its host")
	}
	return nil
}

func Exec(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			methodNotAllowed(w, http.MethodGet)
			return
		}
		if err := serveExec(ctx, d, c, key, w, r); err != nil {
			writeError(w, err)
		}
	}
}

func serveExec(ctx context.Context, d Deps, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) error {
	if err := checkExecRequest(r); err != nil {
		return err
	}
	query, err := ParseExecQuery(r.URL.Query())
	if err != nil {
		return err
	}
	container, err := loadContainer(ctx, c, key)
	if err != nil {
		return err
	}
	pods, err := podsOf(ctx, c, container)
	if err != nil {
		return err
	}
	pod, err := PickReadyPod(pods, query.Pod)
	if err != nil {
		return err
	}
	target, err := ExecTarget(d.Config, key.Namespace, pod.Name, query)
	if err != nil {
		return internalError(err)
	}
	transport, err := rest.TransportFor(rest.CopyConfig(d.Config))
	if err != nil {
		return internalError(err)
	}
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = target
			pr.Out.Host = target.Host
			pr.Out.Header = handshakeHeaders(pr.In.Header)
		},
		FlushInterval: -1,
		Transport:     transport,
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, err error) {
			writeError(w, unreachable(err))
		},
	}
	proxy.ServeHTTP(w, r)
	return nil
}
