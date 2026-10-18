package compute

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/kinds"
)

var requiredKinds = []schema.GroupVersionKind{
	kubeVirtVMGVK,
	kubeVirtVMIGVK,
	kubeVirtMigrationGVK,
	vmSnapshotGVK,
	dataVolumeGVK,
	csiSnapshotGVK,
	instanceTypeGVK,
	preferenceGVK,
	fabricIPGVK,
}

func SetupWithManager(mgr ctrl.Manager) error {
	return kinds.SetupWhenServed(mgr, "compute", requiredKinds, setupControllers)
}

func setupControllers(mgr ctrl.Manager) error {
	vms := &VirtualMachineReconciler{Client: mgr.GetClient(), Secrets: mgr.GetAPIReader(), FabricIPs: mgr.GetCache(), Scheme: mgr.GetScheme(), GuestInfo: guestInfoFrom(mgr.GetConfig()), Now: time.Now}
	for _, setup := range []func(ctrl.Manager) error{
		vms.SetupWithManager,
		(&VolumeReconciler{Client: mgr.GetClient()}).SetupWithManager,
		(&VolumeSnapshotReconciler{Client: mgr.GetClient(), Now: time.Now}).SetupWithManager,
		(&SSHKeyReconciler{Client: mgr.GetClient()}).SetupWithManager,
	} {
		if err := setup(mgr); err != nil {
			return err
		}
	}
	return nil
}

func guestInfoFrom(cfg *rest.Config) GuestInfoFunc {
	return func(ctx context.Context, namespace, name string) (v1alpha1.VMGuestAgent, error) {
		target, err := kubeVirtURL(cfg, "apis", "subresources.kubevirt.io", "v1", "namespaces", namespace, "virtualmachineinstances", name, "guestosinfo")
		if err != nil {
			return v1alpha1.VMGuestAgent{}, err
		}
		httpClient, err := rest.HTTPClientFor(rest.CopyConfig(cfg))
		if err != nil {
			return v1alpha1.VMGuestAgent{}, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
		if err != nil {
			return v1alpha1.VMGuestAgent{}, err
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			return v1alpha1.VMGuestAgent{}, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return v1alpha1.VMGuestAgent{}, fmt.Errorf("the guest info request answered %d", resp.StatusCode)
		}
		body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return v1alpha1.VMGuestAgent{}, err
		}
		return GuestAgentFrom(body)
	}
}
