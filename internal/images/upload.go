package images

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

type UploadResponse struct {
	URL       string    `json:"url"`
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expiresAt"`
}

var UploadTokenRequestGVK = schema.GroupVersionKind{Group: "upload.cdi.kubevirt.io", Version: "v1beta1", Kind: "UploadTokenRequest"}

func UploadURL(host string) string {
	return "https://upload." + host + "/v1beta1/upload-async"
}

func RegisterSubresources(reg *proxy.Subresources) error {
	return reg.Register("images", "upload", UploadHandler(time.Now))
}

func UploadHandler(now func() time.Time) proxy.SubresourceHandler {
	return func(ctx context.Context, c client.Client, key client.ObjectKey, w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeUploadError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if status, message := uploadRefusal(ctx, c, key); status != 0 {
			writeUploadError(w, status, message)
			return
		}
		host, err := PlatformHost(ctx, c)
		if err != nil {
			log.FromContext(ctx).Error(err, "read the platform host")
			writeUploadError(w, http.StatusInternalServerError, "the platform host cannot be read")
			return
		}
		if host == "" {
			writeUploadError(w, http.StatusServiceUnavailable, "platform.host is not set")
			return
		}
		token, err := requestUploadToken(ctx, c, key)
		if err != nil {
			log.FromContext(ctx).Error(err, "request an upload token", "image", key.String())
			writeUploadError(w, http.StatusBadGateway, "the upload service refused the request")
			return
		}
		writeUploadJSON(w, http.StatusOK, UploadResponse{URL: UploadURL(host), Token: token, ExpiresAt: now().Add(UploadTokenTTL)})
	}
}

func uploadRefusal(ctx context.Context, c client.Reader, key client.ObjectKey) (int, string) {
	var img v1alpha1.Image
	if err := c.Get(ctx, key, &img); err != nil {
		if apierrors.IsNotFound(err) {
			return http.StatusNotFound, "image not found"
		}
		log.FromContext(ctx).Error(err, "read the image", "image", key.String())
		return http.StatusInternalServerError, "the image cannot be read"
	}
	if img.Spec.Source.Upload == nil {
		return http.StatusConflict, "image has no upload source"
	}
	if img.Status.Phase != v1alpha1.ImagePhaseWaitingForUpload {
		phase := img.Status.Phase
		if phase == "" {
			phase = v1alpha1.ImagePhasePending
		}
		return http.StatusConflict, fmt.Sprintf("image is %s, not %s", phase, v1alpha1.ImagePhaseWaitingForUpload)
	}
	return 0, ""
}

func requestUploadToken(ctx context.Context, c client.Client, key client.ObjectKey) (string, error) {
	request := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": UploadTokenRequestGVK.GroupVersion().String(),
		"kind":       UploadTokenRequestGVK.Kind,
		"metadata":   map[string]any{"name": ImportName(key.Name), "namespace": key.Namespace},
		"spec":       map[string]any{"pvcName": ImportName(key.Name)},
	}}
	if err := c.Create(ctx, request); err != nil {
		return "", err
	}
	token, _, _ := unstructured.NestedString(request.Object, "status", "token")
	if token == "" {
		return "", fmt.Errorf("the token request of %s holds no token", key)
	}
	return token, nil
}

func writeUploadError(w http.ResponseWriter, status int, message string) {
	writeUploadJSON(w, status, map[string]string{"error": message})
}

func writeUploadJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
