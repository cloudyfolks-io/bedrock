package addons

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const retentionSetting = "loki.retention-days"

var registryInputSettings = []string{"registry.host", "registry.storage-class", "registry.bucket", "registry.http-proxy", "registry.replicas", "database.enabled"}

func kvmOf(host *v1alpha1.Host) *bool {
	if host.Status.Checks == nil {
		return nil
	}
	return host.Status.Checks.KVM
}

func sameKVM(a, b *bool) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func hostKVMChanged(oldHost, newHost *v1alpha1.Host) bool {
	return !sameKVM(kvmOf(oldHost), kvmOf(newHost))
}

func hostPreflightInputsChanged(oldHost, newHost *v1alpha1.Host) bool {
	return hostKVMChanged(oldHost, newHost) || !slices.Equal(oldHost.Spec.Roles, newHost.Spec.Roles)
}

func readSettings() []string {
	names := append([]string{retentionSetting}, registryInputSettings...)
	for _, def := range Known() {
		names = append(names, def.EnabledSetting)
	}
	return names
}

func readSetting(obj client.Object) bool {
	return slices.Contains(readSettings(), obj.GetName())
}

func settingValueChanged(oldSetting, newSetting *v1alpha1.Setting) bool {
	return readSetting(newSetting) && oldSetting.Spec.Value != newSetting.Spec.Value
}

func nodeKVMLabelChanged(oldNode, newNode *corev1.Node) bool {
	oldValue, oldSet := oldNode.Labels[LabelKVM]
	newValue, newSet := newNode.Labels[LabelKVM]
	return oldSet != newSet || oldValue != newValue
}

func hostPreflightPredicate() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return hostPreflightInputsChanged(e.ObjectOld.(*v1alpha1.Host), e.ObjectNew.(*v1alpha1.Host))
	}}
}

func hostKVMPredicate() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return hostKVMChanged(e.ObjectOld.(*v1alpha1.Host), e.ObjectNew.(*v1alpha1.Host))
	}}
}

func readSettingPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return readSetting(e.Object) },
		DeleteFunc:  func(e event.DeleteEvent) bool { return readSetting(e.Object) },
		GenericFunc: func(e event.GenericEvent) bool { return readSetting(e.Object) },
		UpdateFunc: func(e event.UpdateEvent) bool {
			return settingValueChanged(e.ObjectOld.(*v1alpha1.Setting), e.ObjectNew.(*v1alpha1.Setting))
		},
	}
}

func nodeKVMLabelPredicate() predicate.Predicate {
	return predicate.Funcs{UpdateFunc: func(e event.UpdateEvent) bool {
		return nodeKVMLabelChanged(e.ObjectOld.(*corev1.Node), e.ObjectNew.(*corev1.Node))
	}}
}
