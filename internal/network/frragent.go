package network

import (
	"context"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

const (
	frrNamespace = "kube-system"
	frrName      = "fabric-frr"
)

func frrAgentOnly() predicate.Predicate {
	return predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == frrNamespace && obj.GetName() == frrName
	})
}

func frrAgentGap(ctx context.Context, c client.Reader, cfg Config) (*Violation, error) {
	if cfg.EIPMode != EIPModeBGP {
		return nil, nil
	}
	var agent appsv1.DaemonSet
	err := c.Get(ctx, client.ObjectKey{Namespace: frrNamespace, Name: frrName}, &agent)
	if apierrors.IsNotFound(err) {
		return &Violation{Reason: ReasonFRRAgentMissing, Message: "the fabric-frr agent is not installed; enable frr in the fabric chart values"}, nil
	}
	if err != nil {
		return nil, err
	}
	if agent.Status.NumberReady == 0 {
		return &Violation{Reason: ReasonFRRAgentMissing, Message: "no fabric-frr pod is ready; give a Host the fabric-gateway role"}, nil
	}
	return nil, nil
}
