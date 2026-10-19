package database

import (
	"context"
	"fmt"
	"slices"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/container"
	"github.com/cloudyfolks-io/bedrock/internal/network"
)

const StorageClass = "block"

func refusal(reason, format string, args ...any) *Refusal {
	return &Refusal{Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func VersionName(db v1alpha1.Database) string { return db.Spec.Engine + "-" + db.Spec.Version }

type Catalog struct {
	Version v1alpha1.DatabaseVersion
	Class   v1alpha1.DatabaseClass
}

func Instances(class v1alpha1.DatabaseClass) int {
	if class.Spec.HighAvailability {
		return 3
	}
	return 1
}

func resolveCatalog(ctx context.Context, c client.Reader, db v1alpha1.Database) (Catalog, *Refusal, error) {
	var catalog Catalog
	err := c.Get(ctx, client.ObjectKey{Name: VersionName(db)}, &catalog.Version)
	if apierrors.IsNotFound(err) {
		return catalog, refusal(ReasonVersionNotAvailable, "the DatabaseVersion %s does not exist", VersionName(db)), nil
	}
	if err != nil {
		return catalog, nil, err
	}
	err = c.Get(ctx, client.ObjectKey{Name: db.Spec.Class}, &catalog.Class)
	if apierrors.IsNotFound(err) {
		return catalog, refusal(ReasonClassNotAvailable, "the DatabaseClass %s does not exist", db.Spec.Class), nil
	}
	return catalog, nil, err
}

func checkNew(catalog Catalog, db v1alpha1.Database) *Refusal {
	switch {
	case !catalog.Version.Spec.Enabled:
		return refusal(ReasonVersionNotAvailable, "the DatabaseVersion %s is disabled", catalog.Version.Name)
	case !catalog.Class.Spec.Enabled:
		return refusal(ReasonClassNotAvailable, "the DatabaseClass %s is disabled", catalog.Class.Name)
	case db.Spec.StorageGi < catalog.Class.Spec.MinStorageGi || db.Spec.StorageGi > catalog.Class.Spec.MaxStorageGi:
		return refusal(ReasonStorageOutOfRange, "storageGi must be from %d to %d for the class %s", catalog.Class.Spec.MinStorageGi, catalog.Class.Spec.MaxStorageGi, catalog.Class.Name)
	}
	return nil
}

func resolveSubnet(ctx context.Context, c client.Reader, db v1alpha1.Database) (v1alpha1.Subnet, *Refusal, error) {
	var subnet v1alpha1.Subnet
	name := db.Spec.SubnetRef
	if name == "" {
		name = "default"
	}
	err := c.Get(ctx, client.ObjectKey{Namespace: db.Namespace, Name: name}, &subnet)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return subnet, refusal(ReasonSubnetNotFound, "the subnet %q does not exist", name), nil
	}
	if err != nil {
		return subnet, nil, err
	}
	if !meta.IsStatusConditionTrue(subnet.Status.Conditions, v1alpha1.ConditionReady) {
		return subnet, refusal(ReasonSubnetNotReady, "the subnet %q is not ready", name), nil
	}
	return subnet, nil, nil
}

type Pods struct {
	Image        string
	RuntimeClass string
	DNSServers   []string
}

func resolvePods(ctx context.Context, c client.Client, images []string, subnet v1alpha1.Subnet) (Pods, *Refusal, error) {
	var pods Pods
	image, found := EndpointImage(images)
	if !found {
		return pods, refusal(ReasonEndpointImageMissing, "the release holds no HAProxy image"), nil
	}
	pods.Image = image
	name, err := container.Resolver{Client: c}.RuntimeClassName(ctx)
	if err != nil {
		return pods, nil, err
	}
	if name != "" {
		err := c.Get(ctx, client.ObjectKey{Name: name}, &nodev1.RuntimeClass{})
		if apierrors.IsNotFound(err) {
			return pods, refusal(ReasonRuntimeUnavailable, "the RuntimeClass %q does not exist: kata needs a host with KVM, or set the Setting %s to %s", name, container.RuntimeSetting, container.RuntimeNone), nil
		}
		if err != nil {
			return pods, nil, err
		}
		pods.RuntimeClass = name
	}
	networks, err := network.LoadConfig(ctx, c)
	if err != nil {
		return pods, nil, err
	}
	pods.DNSServers = slices.Clone(subnet.Spec.DNSServers)
	if len(pods.DNSServers) == 0 {
		pods.DNSServers = slices.Clone(networks.DNSServers)
	}
	if len(pods.DNSServers) == 0 {
		return pods, refusal(ReasonDNSMissing, "the Setting network.dns-servers is empty"), nil
	}
	return pods, nil, nil
}

func namespaceExists(ctx context.Context, c client.Reader, name string) (bool, error) {
	err := c.Get(ctx, client.ObjectKey{Name: name}, &corev1.Namespace{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil, err
}
