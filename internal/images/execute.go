package images

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

func Execute(ctx context.Context, c client.Client, p Plan) error {
	for _, obj := range p.Apply {
		if err := ssa.Apply(ctx, c, obj, v1alpha1.ImagesFieldManager); err != nil {
			return err
		}
	}
	for _, obj := range p.Delete {
		if err := client.IgnoreNotFound(c.Delete(ctx, obj)); err != nil {
			return err
		}
	}
	return nil
}
