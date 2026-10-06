package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/fields"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/authn/apiserver"
)

const authnConfigFlag = "--authentication-config"

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
	if wanted, _ := authnRestartWanted(deps.Root, service); !wanted {
		return nil
	}
	if !v1alpha1.AuthnRestartGrantFresh(own, deps.Now()) {
		return nil
	}
	return restartForAuthn(ctx, deps)
}

func nodeUpgrading(ctx context.Context, c client.Client, node string) (bool, error) {
	var list v1alpha1.NodeUpgradeList
	if err := c.List(ctx, &list, client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector("spec.node", node)}); err != nil {
		return false, err
	}
	return len(list.Items) > 0, nil
}

func authnRestartWanted(root string, service k0sService) (wanted, known bool) {
	if service.Unit != k0sControllerUnit {
		return false, true
	}
	dir, ok := apiserverProcessDir(root)
	if !ok {
		return false, false
	}
	start, ok := apiserverStartTime(root)
	if !ok {
		return false, false
	}
	firstEnablement := !hasFlag(dir, authnConfigFlag) && k0sConfigNewerThanAPIServer(root, service)
	return firstEnablement || webhookRestartPending(root, start), true
}

func restartPendingStatus(wanted, known, previous bool) bool {
	if !known {
		return previous
	}
	return wanted
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
	if webhookRestartPending(deps.Root, start) {
		return "kube-apiserver still runs from before " + apiserver.WebhookFile
	}
	return bounded(ctx, deps.ProbeTimeout, func(probeCtx context.Context) string {
		return apiProblem(probeCtx, deps.Exec, deps.Root, "/readyz")
	})
}
