package tenancy

import (
	"cmp"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	resourceRequestsCPU     corev1.ResourceName = "requests.cpu"
	resourceRequestsMemory  corev1.ResourceName = "requests.memory"
	resourceRequestsStorage corev1.ResourceName = "requests.storage"
	resourceCountVMs        corev1.ResourceName = "count/virtualmachines.bedrock.cloudyfolks.io"
	resourceCountContainers corev1.ResourceName = "count/containers.bedrock.cloudyfolks.io"
	resourceCountDatabases  corev1.ResourceName = "count/databases.bedrock.cloudyfolks.io"
)

type Decision struct {
	Admitted     bool
	QuotaAllowed bool
	Reason       string
	Message      string
}

type allocation struct {
	count   int32
	cpu     resource.Quantity
	memory  resource.Quantity
	storage resource.Quantity
}

func Decide(ceiling v1alpha1.TenantQuota, projects []v1alpha1.Project, name string) Decision {
	running := allocation{}
	for _, project := range inDecisionOrder(projects) {
		next := running.with(project.Spec.Quota)
		decision := judge(ceiling, next, project)
		if project.Name == name {
			return decision
		}
		running = advance(running, next, project, decision)
	}
	return Decision{}
}

func advance(running, next allocation, project v1alpha1.Project, decision Decision) allocation {
	switch {
	case holdsNoSlot(project):
		return running
	case decision.Admitted && decision.QuotaAllowed:
		return next
	case decision.Admitted:
		return allocation{count: running.count + 1, cpu: running.cpu, memory: running.memory, storage: running.storage}
	}
	return running
}

func holdsNoSlot(project v1alpha1.Project) bool {
	if provisioned(project) {
		return false
	}
	condition := meta.FindStatusCondition(project.Status.Conditions, ConditionNamespaceReady)
	return condition != nil && condition.Status == metav1.ConditionFalse && condition.Reason == ReasonNamespaceConflict
}

func QuotaHard(quota v1alpha1.ProjectQuota) corev1.ResourceList {
	hard := corev1.ResourceList{}
	if quota.CPU != nil {
		hard[resourceRequestsCPU] = quota.CPU.DeepCopy()
	}
	if quota.Memory != nil {
		hard[resourceRequestsMemory] = quota.Memory.DeepCopy()
	}
	if quota.Storage != nil {
		hard[resourceRequestsStorage] = quota.Storage.DeepCopy()
	}
	if quota.VMs != nil {
		hard[resourceCountVMs] = *resource.NewQuantity(int64(*quota.VMs), resource.DecimalSI)
	}
	if quota.Containers != nil {
		hard[resourceCountContainers] = *resource.NewQuantity(int64(*quota.Containers), resource.DecimalSI)
	}
	if quota.Databases != nil {
		hard[resourceCountDatabases] = *resource.NewQuantity(int64(*quota.Databases), resource.DecimalSI)
	}
	return hard
}

func ProjectUsage(used corev1.ResourceList) v1alpha1.ProjectQuota {
	usage := v1alpha1.ProjectQuota{}
	if value, found := used[resourceRequestsCPU]; found {
		usage.CPU = ptr.To(value.DeepCopy())
	}
	if value, found := used[resourceRequestsMemory]; found {
		usage.Memory = ptr.To(value.DeepCopy())
	}
	if value, found := used[resourceRequestsStorage]; found {
		usage.Storage = ptr.To(value.DeepCopy())
	}
	if value, found := used[resourceCountVMs]; found {
		usage.VMs = ptr.To(int32(value.Value()))
	}
	if value, found := used[resourceCountContainers]; found {
		usage.Containers = ptr.To(int32(value.Value()))
	}
	if value, found := used[resourceCountDatabases]; found {
		usage.Databases = ptr.To(int32(value.Value()))
	}
	return usage
}

func TenantUsage(projects []v1alpha1.Project, elasticIPs int) v1alpha1.TenantQuota {
	total := allocation{}
	for _, project := range projects {
		usage := project.Status.Usage
		total = allocation{
			count:   total.count + 1,
			cpu:     sum(total.cpu, usage.CPU),
			memory:  sum(total.memory, usage.Memory),
			storage: sum(total.storage, usage.Storage),
		}
	}
	return v1alpha1.TenantQuota{
		CPU:        &total.cpu,
		Memory:     &total.memory,
		Storage:    &total.storage,
		Projects:   ptr.To(total.count),
		ElasticIPs: ptr.To(int32(elasticIPs)),
	}
}

func (a allocation) with(quota v1alpha1.ProjectQuota) allocation {
	return allocation{
		count:   a.count + 1,
		cpu:     sum(a.cpu, quota.CPU),
		memory:  sum(a.memory, quota.Memory),
		storage: sum(a.storage, quota.Storage),
	}
}

func sum(base resource.Quantity, extra *resource.Quantity) resource.Quantity {
	total := base.DeepCopy()
	if extra != nil {
		total.Add(*extra)
	}
	return total
}

func provisioned(project v1alpha1.Project) bool {
	return project.Status.Namespace != ""
}

func inDecisionOrder(projects []v1alpha1.Project) []v1alpha1.Project {
	ordered := slices.Clone(projects)
	slices.SortStableFunc(ordered, func(a, b v1alpha1.Project) int {
		if byState := cmp.Compare(boolRank(provisioned(b)), boolRank(provisioned(a))); byState != 0 {
			return byState
		}
		if byTime := a.CreationTimestamp.Time.Compare(b.CreationTimestamp.Time); byTime != 0 {
			return byTime
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return ordered
}

func boolRank(value bool) int {
	if value {
		return 1
	}
	return 0
}

func judge(ceiling v1alpha1.TenantQuota, next allocation, project v1alpha1.Project) Decision {
	tenant := project.Spec.TenantRef
	over := overAllocated(ceiling, next)
	missing := missingRequired(ceiling, project.Spec.Quota)
	overAllocation := Decision{Reason: ReasonTenantQuotaExceeded, Message: fmt.Sprintf("tenant %s has no %s left to allocate", tenant, over)}
	required := Decision{Reason: ReasonQuotaRequired, Message: fmt.Sprintf("the project must set %s because tenant %s limits it", missing, tenant)}
	if provisioned(project) {
		switch {
		case missing != "":
			required.Admitted = true
			return required
		case over != "":
			overAllocation.Admitted = true
			return overAllocation
		}
		return Decision{Admitted: true, QuotaAllowed: true}
	}
	switch {
	case ceiling.Projects != nil && next.count > *ceiling.Projects:
		return Decision{Reason: ReasonProjectQuotaExceeded, Message: fmt.Sprintf("tenant %s allows %d projects", tenant, *ceiling.Projects)}
	case missing != "":
		return required
	case over != "":
		return overAllocation
	}
	return Decision{Admitted: true, QuotaAllowed: true}
}

func missingRequired(ceiling v1alpha1.TenantQuota, quota v1alpha1.ProjectQuota) string {
	switch {
	case ceiling.CPU != nil && quota.CPU == nil:
		return "cpu"
	case ceiling.Memory != nil && quota.Memory == nil:
		return "memory"
	case ceiling.Storage != nil && quota.Storage == nil:
		return "storage"
	}
	return ""
}

func overAllocated(ceiling v1alpha1.TenantQuota, next allocation) string {
	switch {
	case exceeds(ceiling.CPU, next.cpu):
		return "cpu"
	case exceeds(ceiling.Memory, next.memory):
		return "memory"
	case exceeds(ceiling.Storage, next.storage):
		return "storage"
	}
	return ""
}

func exceeds(ceiling *resource.Quantity, total resource.Quantity) bool {
	return ceiling != nil && total.Cmp(*ceiling) > 0
}
