package tenancy

import (
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ManageVerb        = "manage"
	DefaultSubnetName = "default"
	DefaultPolicyName = "default"
	QuotaName         = "project-quota"
	GatingLabel       = "application-aware-quota/enable-gating"
	SubnetPrefixBits  = 24
	Interval          = 30 * time.Second
)

var QuotaGVK = schema.GroupVersionKind{Group: "aaq.kubevirt.io", Version: "v1alpha1", Kind: "ApplicationAwareResourceQuota"}

func ProjectObjectLabels(project v1alpha1.Project) map[string]string {
	return v1alpha1.TenantLabels("Project", project.Name, project.Spec.TenantRef, project.Name)
}

func TenantObjectLabels(tenant v1alpha1.Tenant) map[string]string {
	return v1alpha1.TenantLabels("Tenant", tenant.Name, tenant.Name, "")
}
