package agent

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	authnRestartLease    = "bedrock-authn-apiserver-restart"
	authnRestartDuration = 15 * time.Minute
	authnConfigFlag      = "--authentication-config"
)

var authnRestartLeaseKey = client.ObjectKey{Namespace: release.SystemNamespace, Name: authnRestartLease}

func EnableAuthnArgs(ctx context.Context, c client.Client, deps Deps) error {
	var own v1alpha1.Host
	if err := c.Get(ctx, client.ObjectKey{Name: deps.Node}, &own); err != nil {
		return client.IgnoreNotFound(err)
	}
	service := k0sServiceOf(own)
	if service.Unit != k0sControllerUnit || !authnFilesPresent(deps.Root) {
		return nil
	}
	upgrading, err := nodeUpgrading(ctx, c, deps.Node)
	if err != nil || upgrading {
		return err
	}
	if _, err := ensureAuthnArgs(deps.Root, service); err != nil {
		return err
	}
	if !authnRestartNeeded(deps.Root, service) {
		return nil
	}
	held, err := acquireRestartLease(ctx, c, deps.Node, deps.Now())
	if err != nil || !held {
		return err
	}
	restarted := restartForAuthn(ctx, deps)
	return errors.Join(restarted, releaseRestartLease(ctx, c, deps))
}

func nodeUpgrading(ctx context.Context, c client.Client, node string) (bool, error) {
	var list v1alpha1.NodeUpgradeList
	if err := c.List(ctx, &list, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.node", node)}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

func authnRestartNeeded(root string, service k0sService) bool {
	dir, ok := apiserverProcessDir(root)
	return ok && !hasFlag(dir, authnConfigFlag) && k0sConfigNewerThanAPIServer(root, service)
}

func hasFlag(dir, flag string) bool {
	raw, err := os.ReadFile(filepath.Join(dir, "cmdline"))
	if err != nil {
		return false
	}
	return slices.ContainsFunc(strings.Split(string(raw), "\x00"), func(arg string) bool {
		return arg == flag || strings.HasPrefix(arg, flag+"=")
	})
}

func restartForAuthn(ctx context.Context, deps Deps) error {
	if _, err := deps.Exec.Run(ctx, "systemctl", "restart", "--no-block", k0sControllerUnit); err != nil {
		return err
	}
	observed, err := waitClear(ctx, deps.K0sTimeout, deps.K0sPoll, func(probeCtx context.Context) string {
		return authnRestartProblem(probeCtx, deps)
	})
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("kube-apiserver did not restart within %s: %s", deps.K0sTimeout, observed)
	}
	return err
}

func authnRestartProblem(ctx context.Context, deps Deps) string {
	start, ok := apiserverStartTime(deps.Root)
	if !ok {
		return "kube-apiserver does not run"
	}
	if configNewerThan(deps.Root, start) {
		return "kube-apiserver still runs from before " + k0sConfigFile
	}
	return bounded(ctx, deps.ProbeTimeout, func(probeCtx context.Context) string {
		return apiProblem(probeCtx, deps.Exec, deps.Root, "/readyz")
	})
}

func acquireRestartLease(ctx context.Context, c client.Client, holder string, now time.Time) (bool, error) {
	var lease coordinationv1.Lease
	if err := c.Get(ctx, authnRestartLeaseKey, &lease); err != nil {
		return false, fmt.Errorf("lease %s: %w", authnRestartLeaseKey, err)
	}
	if !leaseFree(lease, holder, now) {
		return false, nil
	}
	claimed := claimLease(lease, holder, now)
	err := c.Update(ctx, &claimed)
	if apierrors.IsConflict(err) {
		return false, nil
	}
	return err == nil, err
}

func leaseFree(lease coordinationv1.Lease, holder string, now time.Time) bool {
	current := ptr.Deref(lease.Spec.HolderIdentity, "")
	if current == "" || current == holder || lease.Spec.RenewTime == nil || lease.Spec.LeaseDurationSeconds == nil {
		return true
	}
	return now.After(lease.Spec.RenewTime.Add(time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second))
}

func claimLease(lease coordinationv1.Lease, holder string, now time.Time) coordinationv1.Lease {
	claimed := *lease.DeepCopy()
	stamp := metav1.NewMicroTime(now)
	claimed.Spec.HolderIdentity = ptr.To(holder)
	claimed.Spec.AcquireTime = &stamp
	claimed.Spec.RenewTime = &stamp
	claimed.Spec.LeaseDurationSeconds = ptr.To(int32(authnRestartDuration / time.Second))
	return claimed
}

func releaseRestartLease(ctx context.Context, c client.Client, deps Deps) error {
	return retryCall(ctx, restartAttempts, deps.K0sPoll, func() error {
		var lease coordinationv1.Lease
		if err := c.Get(ctx, authnRestartLeaseKey, &lease); err != nil {
			return err
		}
		if ptr.Deref(lease.Spec.HolderIdentity, "") != deps.Node {
			return nil
		}
		released := *lease.DeepCopy()
		released.Spec.HolderIdentity = nil
		released.Spec.AcquireTime = nil
		released.Spec.RenewTime = nil
		return c.Update(ctx, &released)
	})
}
