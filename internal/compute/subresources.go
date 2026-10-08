package compute

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

const (
	subresourceKind = "virtualmachines"
	maxActionBody   = 1 << 10
)

type action func(ctx context.Context, d Deps, c client.Client, key client.ObjectKey) (Result, error)

func RegisterSubresources(reg *proxy.Subresources) error {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	return registerWith(reg, NewDeps(cfg))
}

func registerWith(reg *proxy.Subresources, d Deps) error {
	handlers := []struct {
		name    string
		handler proxy.SubresourceHandler
	}{
		{"start", postOnly(d, Start)},
		{"stop", postOnly(d, Stop)},
		{"restart", postOnly(d, Restart)},
		{"migrate", postOnly(d, Migrate)},
		{"snapshot", snapshotHandler(d)},
		{"console/vnc", proxy.SubresourceHandler(VNC(d))},
		{"console/serial", proxy.SubresourceHandler(Serial(d))},
	}
	for _, entry := range handlers {
		if err := reg.Register(subresourceKind, entry.name, entry.handler); err != nil {
			return err
		}
	}
	return nil
}

func respond(w http.ResponseWriter, result Result, err error) {
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, result.Code, result.Body)
}

func methodNotAllowed(w http.ResponseWriter, allowed string) {
	w.Header().Set("Allow", allowed)
	writeError(w, ActionError{Code: http.StatusMethodNotAllowed, Reason: "MethodNotAllowed", Message: "the method is not allowed"})
}

func postOnly(d Deps, run action) proxy.SubresourceHandler {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		result, err := run(ctx, d, c, key)
		respond(w, result, err)
	}
}

func snapshotHandler(d Deps) proxy.SubresourceHandler {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			result, err := ListSnapshots(ctx, d, c, key)
			respond(w, result, err)
		case http.MethodPost:
			retention, err := retentionOf(r)
			if err != nil {
				writeError(w, err)
				return
			}
			result, err := TakeSnapshot(ctx, d, c, key, retention)
			respond(w, result, err)
		default:
			methodNotAllowed(w, "GET, POST")
		}
	}
}

func retentionOf(r *http.Request) (time.Duration, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, maxActionBody+1))
	if err != nil {
		return 0, badRequest("the request body cannot be read")
	}
	if len(body) > maxActionBody {
		return 0, badRequest("the request body is too large")
	}
	var request struct {
		Retention string `json:"retention"`
	}
	if len(bytes.TrimSpace(body)) > 0 {
		if err := json.Unmarshal(body, &request); err != nil {
			return 0, badRequest("the request body is not valid JSON")
		}
	}
	retention, err := ParseRetention(request.Retention)
	if err != nil {
		return 0, badRequest("%s", err.Error())
	}
	return retention, nil
}
