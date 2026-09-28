package agent

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func ApplyStatus(ctx context.Context, c client.Client, name string, status v1alpha1.HostStatus) error {
	desired := &v1alpha1.Host{
		TypeMeta:   metav1.TypeMeta{APIVersion: v1alpha1.GroupVersion.String(), Kind: "Host"},
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status:     status,
	}
	return ssa.ApplyStatus(ctx, c, desired, v1alpha1.AgentFieldManager)
}
