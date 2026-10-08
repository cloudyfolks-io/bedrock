package compute

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	defaultRetention = 168 * time.Hour
	maxRetention     = 720 * time.Hour
	snapshotStamp    = "20060102150405"
)

type Deps struct {
	Config *rest.Config
	Now    func() time.Time
}

type Result struct {
	Code int
	Body any
}

type ActionError struct {
	Code    int
	Reason  metav1.StatusReason
	Message string
}

type SnapshotInfo struct {
	Name       string `json:"name"`
	Phase      string `json:"phase,omitempty"`
	ReadyToUse bool   `json:"readyToUse"`
	CreatedAt  string `json:"createdAt,omitempty"`
	ExpiresAt  string `json:"expiresAt,omitempty"`
}

func (e ActionError) Error() string {
	return e.Message
}

func (e ActionError) Status() metav1.Status {
	return metav1.Status{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
		Status:   metav1.StatusFailure,
		Code:     int32(e.Code),
		Reason:   e.Reason,
		Message:  e.Message,
	}
}

func NewDeps(cfg *rest.Config) Deps {
	return Deps{Config: cfg, Now: time.Now}
}

func ParseRetention(raw string) (time.Duration, error) {
	if raw == "" {
		return defaultRetention, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("the retention is not a duration: %w", err)
	}
	if parsed <= 0 || parsed > maxRetention {
		return 0, fmt.Errorf("the retention must be longer than zero and at most %s", maxRetention)
	}
	return parsed, nil
}

func badRequest(format string, args ...any) ActionError {
	return ActionError{Code: http.StatusBadRequest, Reason: metav1.StatusReasonBadRequest, Message: fmt.Sprintf(format, args...)}
}

func conflict(format string, args ...any) ActionError {
	return ActionError{Code: http.StatusConflict, Reason: metav1.StatusReasonConflict, Message: fmt.Sprintf(format, args...)}
}

func internalError(err error) ActionError {
	logf.Log.WithName("compute").Error(err, "the request failed")
	return ActionError{Code: http.StatusInternalServerError, Reason: metav1.StatusReasonInternalError, Message: "the request failed; ask an administrator to read the operator log"}
}

func unreachable(err error) ActionError {
	logf.Log.WithName("compute").Error(err, "KubeVirt is not reachable")
	return ActionError{Code: http.StatusBadGateway, Reason: metav1.StatusReasonServiceUnavailable, Message: "KubeVirt did not answer the request"}
}

func accepted() Result {
	return Result{Code: http.StatusAccepted, Body: metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusSuccess, Code: http.StatusAccepted}}
}

func loadVM(ctx context.Context, c client.Client, key client.ObjectKey) (v1alpha1.VirtualMachine, error) {
	var vm v1alpha1.VirtualMachine
	if len(validation.IsDNS1123Label(key.Namespace)) > 0 || len(validation.IsDNS1123Label(key.Name)) > 0 {
		return vm, badRequest("the namespace and the name must be DNS labels")
	}
	err := c.Get(ctx, key, &vm)
	if apierrors.IsNotFound(err) {
		return vm, ActionError{Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound, Message: fmt.Sprintf("the virtual machine %q does not exist", key.Name)}
	}
	if err != nil {
		return vm, internalError(err)
	}
	return vm, nil
}

func requireRunning(vm v1alpha1.VirtualMachine) error {
	if vm.Status.Phase != v1alpha1.VMPhaseRunning {
		return conflict("the virtual machine is %s and must be Running", orUnknown(vm.Status.Phase))
	}
	return nil
}

func orUnknown(phase string) string {
	if phase == "" {
		return "not running"
	}
	return phase
}

func ownerLabelsOf(ctx context.Context, c client.Client, vm v1alpha1.VirtualMachine) (map[string]string, error) {
	var ns corev1.Namespace
	if err := c.Get(ctx, client.ObjectKey{Name: vm.Namespace}, &ns); err != nil {
		return nil, internalError(err)
	}
	labels, ok := OwnerLabels("VirtualMachine", vm.Name, ns)
	if !ok {
		return nil, ActionError{Code: http.StatusNotFound, Reason: metav1.StatusReasonNotFound, Message: "the namespace is not a project namespace"}
	}
	return labels, nil
}

func plainOwnerRef(vm v1alpha1.VirtualMachine) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: v1alpha1.GroupVersion.String(), Kind: "VirtualMachine", Name: vm.Name, UID: vm.UID}
}

func setRunning(ctx context.Context, c client.Client, key client.ObjectKey, running bool) (Result, error) {
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		return Result{}, err
	}
	if vm.Spec.Running == running {
		return accepted(), nil
	}
	patch, err := rawMergePatch(map[string]any{"spec": map[string]any{"running": running}})
	if err != nil {
		return Result{}, internalError(err)
	}
	if err := c.Patch(ctx, &vm, patch, client.FieldOwner(v1alpha1.ComputeFieldManager)); err != nil {
		return Result{}, internalError(err)
	}
	return accepted(), nil
}

func Start(ctx context.Context, _ Deps, c client.Client, key client.ObjectKey) (Result, error) {
	return setRunning(ctx, c, key, true)
}

func Stop(ctx context.Context, _ Deps, c client.Client, key client.ObjectKey) (Result, error) {
	return setRunning(ctx, c, key, false)
}

func Restart(ctx context.Context, d Deps, c client.Client, key client.ObjectKey) (Result, error) {
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		return Result{}, err
	}
	if err := requireRunning(vm); err != nil {
		return Result{}, err
	}
	target, err := kubeVirtSubresourceURL(d.Config, key, "restart")
	if err != nil {
		return Result{}, internalError(err)
	}
	httpClient, err := rest.HTTPClientFor(rest.CopyConfig(d.Config))
	if err != nil {
		return Result{}, internalError(err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, target, nil)
	if err != nil {
		return Result{}, internalError(err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Result{}, unreachable(fmt.Errorf("the restart answered %d", resp.StatusCode))
	}
	return accepted(), nil
}

func kubeVirtURL(cfg *rest.Config, elements ...string) (*url.URL, error) {
	base, _, err := rest.DefaultServerURL(cfg.Host, cfg.APIPath, schema.GroupVersion{}, rest.IsConfigTransportTLS(*cfg))
	if err != nil {
		return nil, err
	}
	joined := *base
	joined.Path = path.Join(append([]string{"/", base.Path}, elements...)...)
	return &joined, nil
}

func kubeVirtSubresourceURL(cfg *rest.Config, key client.ObjectKey, action string) (string, error) {
	target, err := kubeVirtURL(cfg, "apis", "subresources.kubevirt.io", "v1", "namespaces", key.Namespace, "virtualmachines", key.Name, action)
	if err != nil {
		return "", err
	}
	return target.String(), nil
}

func Migrate(ctx context.Context, _ Deps, c client.Client, key client.ObjectKey) (Result, error) {
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		return Result{}, err
	}
	if err := requireRunning(vm); err != nil {
		return Result{}, err
	}
	labels, err := ownerLabelsOf(ctx, c, vm)
	if err != nil {
		return Result{}, err
	}
	migration := emptyOf(kubeVirtMigrationGVK)
	migration.SetNamespace(vm.Namespace)
	migration.SetGenerateName(vm.Name + "-migrate-")
	migration.SetLabels(labels)
	migration.SetOwnerReferences([]metav1.OwnerReference{plainOwnerRef(vm)})
	migration.Object["spec"] = map[string]any{"vmiName": vm.Name}
	if err := c.Create(ctx, migration, client.FieldOwner(v1alpha1.ComputeFieldManager)); err != nil {
		return Result{}, internalError(err)
	}
	result := accepted()
	result.Body = map[string]string{"migration": migration.GetName()}
	return result, nil
}

func TakeSnapshot(ctx context.Context, d Deps, c client.Client, key client.ObjectKey, retention time.Duration) (Result, error) {
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		return Result{}, err
	}
	labels, err := ownerLabelsOf(ctx, c, vm)
	if err != nil {
		return Result{}, err
	}
	now := d.Now().UTC()
	expires := now.Add(retention).Format(time.RFC3339)
	snapshot := emptyOf(vmSnapshotGVK)
	snapshot.SetNamespace(vm.Namespace)
	snapshot.SetName(vm.Name + "-" + now.Format(snapshotStamp))
	snapshot.SetLabels(labels)
	snapshot.SetAnnotations(map[string]string{AnnotationExpiresAt: expires})
	snapshot.SetOwnerReferences([]metav1.OwnerReference{plainOwnerRef(vm)})
	snapshot.Object["spec"] = map[string]any{"source": map[string]any{"apiGroup": "kubevirt.io", "kind": "VirtualMachine", "name": vm.Name}}
	if err := c.Create(ctx, snapshot, client.FieldOwner(v1alpha1.ComputeFieldManager)); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return Result{}, conflict("a snapshot of this second exists already")
		}
		return Result{}, internalError(err)
	}
	return Result{Code: http.StatusCreated, Body: map[string]string{"name": snapshot.GetName(), "expiresAt": expires}}, nil
}

func ListSnapshots(ctx context.Context, _ Deps, c client.Client, key client.ObjectKey) (Result, error) {
	vm, err := loadVM(ctx, c, key)
	if err != nil {
		return Result{}, err
	}
	list := &unstructured.UnstructuredList{}
	list.SetGroupVersionKind(vmSnapshotGVK.GroupVersion().WithKind("VirtualMachineSnapshotList"))
	selector := client.MatchingLabels{v1alpha1.LabelKind: "VirtualMachine", v1alpha1.LabelName: vm.Name}
	if err := c.List(ctx, list, client.InNamespace(vm.Namespace), selector); err != nil {
		return Result{}, internalError(err)
	}
	infos := make([]SnapshotInfo, 0, len(list.Items))
	for _, item := range list.Items {
		phase, _, _ := unstructured.NestedString(item.Object, "status", "phase")
		ready, _, _ := unstructured.NestedBool(item.Object, "status", "readyToUse")
		infos = append(infos, SnapshotInfo{
			Name:       item.GetName(),
			Phase:      phase,
			ReadyToUse: ready,
			CreatedAt:  item.GetCreationTimestamp().UTC().Format(time.RFC3339),
			ExpiresAt:  item.GetAnnotations()[AnnotationExpiresAt],
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return Result{Code: http.StatusOK, Body: infos}, nil
}
