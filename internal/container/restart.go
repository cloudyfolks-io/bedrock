package container

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

type RestartResult struct {
	RestartedAt string `json:"restartedAt"`
}

func Restart(d Deps) func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		result, err := RestartContainer(ctx, d, c, key)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusAccepted, result)
	}
}

func RestartContainer(ctx context.Context, d Deps, c client.Client, key client.ObjectKey) (RestartResult, error) {
	container, err := loadContainer(ctx, c, key)
	if err != nil {
		return RestartResult{}, err
	}
	stamp := d.now().UTC().Format(time.RFC3339)
	body, err := json.Marshal(map[string]any{"metadata": map[string]any{"annotations": map[string]string{v1alpha1.AnnotationRestartedAt: stamp}}})
	if err != nil {
		return RestartResult{}, internalError(err)
	}
	if err := c.Patch(ctx, &container, client.RawPatch(types.MergePatchType, body), client.FieldOwner(v1alpha1.ProxyFieldManager)); err != nil {
		return RestartResult{}, internalError(err)
	}
	return RestartResult{RestartedAt: stamp}, nil
}
