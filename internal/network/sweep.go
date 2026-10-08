package network

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network/fabric"
)

const (
	SweepInterval = 10 * time.Minute
	SweepMinAge   = time.Minute
)

var sweptKinds = []schema.GroupVersionKind{
	fabric.OvnFipGVK, fabric.RouterLBRuleGVK, fabric.SwitchLBRuleGVK, fabric.DNSZoneGVK, fabric.VipGVK, fabric.OvnEipGVK,
	fabric.SecurityGroupGVK, fabric.SubnetGVK, fabric.VpcGVK,
}

var deletionRank = map[string]int{
	fabric.OvnFipGVK.Kind: 0, fabric.RouterLBRuleGVK.Kind: 0, fabric.SwitchLBRuleGVK.Kind: 0, fabric.DNSZoneGVK.Kind: 0, fabric.VipGVK.Kind: 1,
	fabric.OvnEipGVK.Kind: 1, fabric.SecurityGroupGVK.Kind: 2, fabric.SubnetGVK.Kind: 3, fabric.VpcGVK.Kind: 4,
}

type OwnerKey struct {
	Kind      string
	Namespace string
	Name      string
}

type Orphan struct {
	GVK   schema.GroupVersionKind
	Name  string
	Owner OwnerKey
	UID   types.UID
}

func ownerOfManaged(u *unstructured.Unstructured) (OwnerKey, types.UID, bool) {
	labels := u.GetLabels()
	if key, uid, ok := OwnerOf(u); ok {
		return OwnerKey{Kind: labels[v1alpha1.LabelKind], Namespace: key.Namespace, Name: key.Name}, uid, true
	}
	if labels[v1alpha1.LabelKind] == "Namespace" && u.GetName() == DefaultSecurityGroupName(labels[v1alpha1.LabelName]) {
		return OwnerKey{Kind: "Namespace", Name: labels[v1alpha1.LabelName]}, "", true
	}
	return OwnerKey{}, "", false
}

func isOrphan(u *unstructured.Unstructured, owners map[OwnerKey]types.UID, now time.Time, minAge time.Duration) (Orphan, bool) {
	labels := u.GetLabels()
	if labels[LabelManaged] != "true" || labels[LabelComponent] == ComponentUnderlay || now.Sub(u.GetCreationTimestamp().Time) < minAge {
		return Orphan{}, false
	}
	key, uid, ok := ownerOfManaged(u)
	if !ok {
		return Orphan{}, false
	}
	live, found := owners[key]
	if found && (uid == "" || live == uid) {
		return Orphan{}, false
	}
	return Orphan{GVK: u.GroupVersionKind(), Name: u.GetName(), Owner: key, UID: uid}, true
}

func FindOrphans(objects []*unstructured.Unstructured, owners map[OwnerKey]types.UID, now time.Time, minAge time.Duration) []Orphan {
	var orphans []Orphan
	for _, u := range objects {
		if orphan, found := isOrphan(u, owners, now, minAge); found {
			orphans = append(orphans, orphan)
		}
	}
	slices.SortStableFunc(orphans, func(a, b Orphan) int {
		return cmp.Or(cmp.Compare(deletionRank[a.GVK.Kind], deletionRank[b.GVK.Kind]), cmp.Compare(a.Name, b.Name))
	})
	return orphans
}

type Sweeper struct {
	Client   client.Client
	Reader   client.Reader
	Interval time.Duration
	MinAge   time.Duration
	Now      func() time.Time
}

func (s *Sweeper) live() client.Reader {
	if s.Reader != nil {
		return s.Reader
	}
	return s.Client
}

func (s *Sweeper) interval() time.Duration {
	return cmp.Or(s.Interval, SweepInterval)
}

func (s *Sweeper) minAge() time.Duration {
	return cmp.Or(s.MinAge, SweepMinAge)
}

func (s *Sweeper) now() time.Time {
	if s.Now == nil {
		return time.Now()
	}
	return s.Now()
}

func (s *Sweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(s.interval())
	defer ticker.Stop()
	for {
		if err := s.SweepOnce(ctx); err != nil {
			ctrl.LoggerFrom(ctx).Error(err, "sweep the orphaned Fabric objects")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Sweeper) NeedLeaderElection() bool {
	return true
}

func (s *Sweeper) SweepOnce(ctx context.Context) error {
	objects, err := s.managedObjects(ctx)
	if err != nil {
		return err
	}
	owners, err := s.owners(ctx)
	if err != nil {
		return err
	}
	var first error
	for _, orphan := range FindOrphans(objects, owners, s.now(), s.minAge()) {
		gone, err := s.ownerGone(ctx, orphan)
		if err == nil && gone {
			err = deleteObject(ctx, s.Client, orphan.GVK, orphan.Name)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

func ownerGVK(kind string) schema.GroupVersionKind {
	if kind == "Namespace" {
		return schema.GroupVersionKind{Version: "v1", Kind: kind}
	}
	return v1alpha1.GroupVersion.WithKind(kind)
}

func (s *Sweeper) ownerGone(ctx context.Context, orphan Orphan) (bool, error) {
	owner := newObject(ownerGVK(orphan.Owner.Kind), orphan.Owner.Name)
	err := s.live().Get(ctx, client.ObjectKey{Namespace: orphan.Owner.Namespace, Name: orphan.Owner.Name}, owner)
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	return orphan.UID != "" && owner.GetUID() != orphan.UID, nil
}

func (s *Sweeper) managedObjects(ctx context.Context) ([]*unstructured.Unstructured, error) {
	var all []*unstructured.Unstructured
	for _, gvk := range sweptKinds {
		items, err := listObjects(ctx, s.Client, gvk, client.MatchingLabels{LabelManaged: "true"})
		if meta.IsNoMatchError(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		all = append(all, items...)
	}
	return all, nil
}

func (s *Sweeper) owners(ctx context.Context) (map[OwnerKey]types.UID, error) {
	owners := map[OwnerKey]types.UID{}
	var networks v1alpha1.NetworkList
	var subnets v1alpha1.SubnetList
	var eips v1alpha1.ElasticIPList
	var balancers v1alpha1.LoadBalancerList
	var zones v1alpha1.DNSZoneList
	var groups v1alpha1.SecurityGroupList
	var namespaces corev1.NamespaceList
	err := errors.Join(
		s.Client.List(ctx, &namespaces),
		s.Client.List(ctx, &networks), s.Client.List(ctx, &subnets), s.Client.List(ctx, &eips),
		s.Client.List(ctx, &balancers), s.Client.List(ctx, &zones), s.Client.List(ctx, &groups),
	)
	if err != nil {
		return nil, err
	}
	record := func(kind string, objects ...client.Object) {
		for _, o := range objects {
			owners[OwnerKey{Kind: kind, Namespace: o.GetNamespace(), Name: o.GetName()}] = o.GetUID()
		}
	}
	for i := range namespaces.Items {
		record("Namespace", &namespaces.Items[i])
	}
	for i := range networks.Items {
		record("Network", &networks.Items[i])
	}
	for i := range subnets.Items {
		record("Subnet", &subnets.Items[i])
	}
	for i := range eips.Items {
		record("ElasticIP", &eips.Items[i])
	}
	for i := range balancers.Items {
		record("LoadBalancer", &balancers.Items[i])
	}
	for i := range zones.Items {
		record("DNSZone", &zones.Items[i])
	}
	for i := range groups.Items {
		record("SecurityGroup", &groups.Items[i])
	}
	return owners, nil
}
