package addons

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

type podSelector struct {
	Namespace string
	Selector  labels.Selector
}

func selectorsOf([]*unstructured.Unstructured) []podSelector { return nil }

func pruneStep(context.Context, client.Client, release.Inventory, []podSelector) (bool, []string, error) {
	return true, nil, nil
}
