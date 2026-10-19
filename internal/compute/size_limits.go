package compute

import (
	"context"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	settingMaxCores     = "compute.max-cores"
	settingMaxMemoryMiB = "compute.max-memory-mib"
)

func isSizeLimitSetting(name string) bool {
	return name == settingMaxCores || name == settingMaxMemoryMiB
}

func (r *VirtualMachineReconciler) sizeLimit(ctx context.Context, key string) (int32, error) {
	def, _ := settings.Lookup(key)
	value := def.Default
	var setting v1alpha1.Setting
	err := r.Client.Get(ctx, client.ObjectKey{Name: key}, &setting)
	if err != nil && !notFound(err) {
		return 0, err
	}
	if err == nil && setting.Spec.Value != "" && def.Validate(setting.Spec.Value) == nil {
		value = setting.Spec.Value
	}
	parsed, err := strconv.ParseInt(value, 10, 32)
	return int32(parsed), err
}

func (r *VirtualMachineReconciler) computeLimits(ctx context.Context) (ComputeLimits, error) {
	cores, err := r.sizeLimit(ctx, settingMaxCores)
	if err != nil {
		return ComputeLimits{}, err
	}
	memory, err := r.sizeLimit(ctx, settingMaxMemoryMiB)
	return ComputeLimits{MaxCores: cores, MaxMemoryMiB: memory}, err
}

func (r *VirtualMachineReconciler) stepCompute(ctx context.Context, vm v1alpha1.VirtualMachine, in RenderInput) (RenderInput, *Gate, error) {
	if vm.Spec.Compute == nil {
		return in, nil, nil
	}
	limits, err := r.computeLimits(ctx)
	if err != nil {
		return in, nil, err
	}
	if issues := ComputeIssues(vm.Spec.Compute, limits); len(issues) > 0 {
		return in, &Gate{Reason: ReasonInvalidSpec, Message: issueMessage(issues)}, nil
	}
	return in, nil, nil
}

func (r *VirtualMachineReconciler) sizedByCustomCompute(ctx context.Context, obj client.Object) []reconcile.Request {
	if !isSizeLimitSetting(obj.GetName()) {
		return nil
	}
	var vms v1alpha1.VirtualMachineList
	if err := r.Client.List(ctx, &vms); err != nil {
		return nil
	}
	var requests []reconcile.Request
	for _, vm := range vms.Items {
		if vm.Spec.Compute != nil {
			requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(&vm)})
		}
	}
	return requests
}
