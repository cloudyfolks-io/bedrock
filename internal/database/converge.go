package database

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
	"github.com/cloudyfolks-io/bedrock/internal/database/mysql"
	"github.com/cloudyfolks-io/bedrock/internal/database/postgres"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/objectstorage/s3"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/ssa"
)

const (
	annotationReadyOnce      = "bedrock.cloudyfolks.io/ready-once"
	annotationAppliedVersion = "bedrock.cloudyfolks.io/applied-version"
)

type Observed struct {
	EngineReady   bool
	EngineReason  string
	Bootstrapped  bool
	GateReady     bool
	EndpointReady bool
	Instances     int32
	Backup        *v1alpha1.DatabaseBackupStatus
	Refusal       *Refusal
	Upgrading     bool
	Image         string
	Applied       *v1alpha1.DatabaseApplied
	Stats         *v1alpha1.DatabaseStats
}

type flow struct {
	cfg         Config
	db          *v1alpha1.Database
	project     Project
	catalog     Catalog
	subnet      v1alpha1.Subnet
	pods        Pods
	port        int
	legacy      *legacyEndpoint
	exists      bool
	plan        Result
	preBackups  []*unstructured.Unstructured
	image       string
	dest        string
	cluster     *unstructured.Unstructured
	restore     *unstructured.Unstructured
	source      *engine.RestoreBackup
	tlsSecret   string
	tlsRevision string
	customerCA  []byte

	mysqlRuntime   mysqlRuntime
	legacyACLReady bool
}

type mysqlRuntime struct {
	name     string
	overhead corev1.ResourceList
}

func (r *Reconciler) converge(ctx context.Context, cfg Config, project Project, db *v1alpha1.Database) (Observed, error) {
	in := &flow{cfg: cfg, db: db, project: project}
	refused, err := r.prepare(ctx, in)
	if err != nil {
		return Observed{}, err
	}
	if refused == nil {
		refused, err = r.apply(ctx, in)
		if err != nil {
			return Observed{}, err
		}
	}
	observed, err := r.observe(ctx, in, refused)
	return observed, err
}

func (r *Reconciler) prepare(ctx context.Context, in *flow) (*Refusal, error) {
	db := *in.db
	cluster, exists, err := r.clusterOf(ctx, db)
	if err != nil {
		return nil, err
	}
	in.exists, in.cluster = exists, cluster
	catalog, refused, err := resolveCatalog(ctx, r.Live, db)
	if err != nil || refused != nil {
		return refused, err
	}
	in.catalog = catalog
	in.port = int(catalog.Version.Spec.Port)
	if !exists {
		if refused := checkNew(catalog, db); refused != nil {
			return refused, nil
		}
		if refused := checkLostEngine(catalog, db); refused != nil {
			return refused, nil
		}
	} else if refused := checkExisting(catalog, db, cluster); refused != nil {
		return refused, nil
	}
	runtime, refused, err := resolveMySQLRuntime(ctx, r.Live, db, cluster, in.cfg.MySQLRuntimeClass)
	if err != nil || refused != nil {
		return refused, err
	}
	in.mysqlRuntime = runtime
	steps := []func(context.Context, *flow) (*Refusal, error){r.preparePlatform, r.prepareCapacity, r.prepareSubnet, r.prepareEndpointAddress, r.prepareCustomerTLS, r.preparePods, r.prepareVIPRange, r.prepareRetention, r.prepareBackupStore, r.prepareRestore, r.prepareOperations}
	for _, step := range steps {
		if refused, err := step(ctx, in); err != nil || refused != nil {
			return refused, err
		}
	}
	return nil, nil
}

func (r *Reconciler) clusterOf(ctx context.Context, db v1alpha1.Database) (*unstructured.Unstructured, bool, error) {
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(primaryGVK(db.Spec.Engine))
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: Name(db.UID)}, primary)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil, false, nil
	}
	return primary, err == nil, err
}

func resolveMySQLRuntime(ctx context.Context, c client.Reader, db v1alpha1.Database, cluster *unstructured.Unstructured, configured string) (mysqlRuntime, *Refusal, error) {
	if db.Spec.Engine != v1alpha1.DatabaseEngineMySQL {
		return mysqlRuntime{}, nil, nil
	}
	if cluster != nil {
		if platform.OwnerOf(cluster).UID != db.UID {
			return mysqlRuntime{}, refusal(ReasonNameTaken, "the MySQL cluster %s exists and this database does not own it", cluster.GetName()), nil
		}
		name, _, err := unstructured.NestedString(cluster.Object, "spec", "mysql", "runtimeClassName")
		return mysqlRuntime{name: name}, nil, err
	}
	if configured == "" {
		return mysqlRuntime{}, nil, nil
	}
	var class nodev1.RuntimeClass
	if err := c.Get(ctx, client.ObjectKey{Name: configured}, &class); err != nil {
		if apierrors.IsNotFound(err) {
			return mysqlRuntime{}, refusal(ReasonRuntimeUnavailable, "the RuntimeClass %q set by %s does not exist", configured, keyMySQLRuntime), nil
		}
		return mysqlRuntime{}, nil, err
	}
	selected := mysqlRuntime{name: class.Name}
	if class.Overhead != nil {
		selected.overhead = class.Overhead.PodFixed
	}
	return selected, nil, nil
}

func (r *Reconciler) preparePlatform(ctx context.Context, in *flow) (*Refusal, error) {
	exists, err := namespaceExists(ctx, r.Live, PlatformNamespace)
	if err != nil || exists {
		return nil, err
	}
	return nil, EnsurePlatform(ctx, r.Client, in.cfg)
}

func (r *Reconciler) prepareCapacity(ctx context.Context, in *flow) (*Refusal, error) {
	if in.exists {
		return nil, nil
	}
	var quota corev1.ResourceQuota
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: QuotaName}, &quota)
	if apierrors.IsNotFound(err) {
		return refusal(ReasonCapacityUnknown, "the resource quota %s of the namespace %s does not exist yet", QuotaName, PlatformNamespace), nil
	}
	if err != nil {
		return nil, err
	}
	instances := int64(Instances(in.catalog.Class))
	cpu, memory := in.catalog.Class.Spec.CPU.DeepCopy(), in.catalog.Class.Spec.Memory.DeepCopy()
	cpu.Add(in.mysqlRuntime.overhead[corev1.ResourceCPU])
	memory.Add(in.mysqlRuntime.overhead[corev1.ResourceMemory])
	cpu.SetMilli(cpu.MilliValue() * instances)
	memory.Set(memory.Value() * instances)
	return CheckCapacity(quota, cpu, memory, in.cfg.MaxUtilization), nil
}

func (r *Reconciler) prepareSubnet(ctx context.Context, in *flow) (*Refusal, error) {
	subnet, refused, err := resolveSubnet(ctx, r.Live, *in.db)
	in.subnet = subnet
	return refused, err
}

func (r *Reconciler) prepareEndpointAddress(ctx context.Context, in *flow) (*Refusal, error) {
	if hasLegacyEndpoint(*in.db) {
		return r.prepareLegacyEndpoint(ctx, in)
	}
	if _, recorded := AddressOf(*in.db, in.subnet.Name); recorded {
		return nil, nil
	}
	if problem := PinnedAddressProblem(*in.db, in.subnet); problem != "" {
		return refusal(ReasonEndpointAddressInvalid, "%s", problem), nil
	}
	return nil, nil
}

func (r *Reconciler) prepareCustomerTLS(ctx context.Context, in *flow) (*Refusal, error) {
	tls, err := importedTLS(ctx, r.Live, *in.db)
	switch {
	case errors.Is(err, errImportBinding):
		return refusal(ReasonApplyFailed, "the imported customer TLS Secret is bound to a different database"), nil
	case errors.Is(err, errMissingImportTLS), errors.Is(err, errInvalidImportTLS):
		return refusal(ReasonApplyFailed, "%s", err), nil
	case err != nil:
		return nil, fmt.Errorf("read the imported customer TLS Secret: %w", err)
	case tls == nil:
		if engineUsesImportedTLS(*in.db, in.cluster) {
			return refusal(ReasonApplyFailed, "%s", errMissingImportTLS), nil
		}
		return nil, nil
	}
	in.tlsSecret = tls.Name
	in.customerCA = tls.Data["ca.crt"]
	in.tlsRevision = configHash(string(tls.Data["tls.crt"]), string(in.customerCA))
	return nil, nil
}

func (r *Reconciler) preparePods(ctx context.Context, in *flow) (*Refusal, error) {
	pods, refused, err := resolvePods(ctx, r.Client, r.Images, in.subnet)
	in.pods = pods
	if err == nil && refused == nil && in.legacy != nil && pods.NIC.Class != network.NetworkClassManaged {
		return refusal(ReasonProxyWaiting, "the legacy frontend needs the managed network interface"), nil
	}
	return refused, err
}

func (r *Reconciler) prepareVIPRange(ctx context.Context, in *flow) (*Refusal, error) {
	if in.legacy != nil || in.cfg.VIPRange.IsValid() {
		return nil, nil
	}
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: VIPName(in.db.UID)}, &corev1.Service{})
	if err == nil {
		return nil, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}
	return refusal(ReasonVIPRangeMissing, "the Setting database.vip-range is empty"), nil
}

func (r *Reconciler) prepareRetention(_ context.Context, in *flow) (*Refusal, error) {
	return RetentionRefusal(in.cfg, in.db.Spec.Backup), nil
}

func (r *Reconciler) prepareBackupStore(ctx context.Context, in *flow) (*Refusal, error) {
	if !in.db.Spec.Backup.Enabled && in.db.Spec.Restore == nil {
		return nil, nil
	}
	ready, err := EnsureBackupStore(ctx, r.Client, r.Store, in.cfg.BackupBucket)
	var failed *s3.Error
	switch {
	case s3.IsUnavailable(err):
		return refusal(ReasonBackupStoreFailed, "the object store is not reachable"), nil
	case errors.As(err, &failed):
		return refusal(ReasonBackupStoreFailed, "the object store refused the backup bucket %s: %s", in.cfg.BackupBucket, failed.Code), nil
	case err != nil:
		return nil, err
	case !ready:
		return refusal(ReasonBackupStorePending, "the identity of the backup store is not ready yet"), nil
	}
	return nil, nil
}

func (r *Reconciler) prepareRestore(ctx context.Context, in *flow) (*Refusal, error) {
	if in.db.Spec.Restore == nil {
		return nil, nil
	}
	source, err := importedRestore(ctx, r.Live, *in.db)
	if err != nil {
		return nil, err
	}
	in.source = source
	if in.db.Annotations[annotationReadyOnce] == "true" {
		return nil, nil
	}
	if !in.exists {
		check, err := LoadRestoreCheck(ctx, r.Live, r.Store, in.cfg, *in.db, r.now())
		if err != nil {
			if s3.IsUnavailable(err) {
				return refusal(ReasonBackupStoreFailed, "the object store is not reachable"), nil
			}
			return nil, err
		}
		if refused := CheckRestore(check); refused != nil {
			return refused, nil
		}
		if refused, err := r.checkSourceUsers(ctx, *in.db); err != nil || refused != nil {
			return refused, err
		}
	}
	if in.db.Spec.Engine != v1alpha1.DatabaseEngineMySQL {
		return nil, nil
	}
	return r.prepareMySQLRestore(ctx, in)
}

func (r *Reconciler) checkSourceUsers(ctx context.Context, db v1alpha1.Database) (*Refusal, error) {
	if db.Spec.Engine != v1alpha1.DatabaseEngineMySQL {
		return nil, nil
	}
	_, found, err := SourceUsers(ctx, r.Live, db.Spec.Restore.SourceUID)
	if err != nil || found {
		return nil, err
	}
	return refusal(ReasonRestoreSourceEmpty, "%s", ErrSourceUsersGone.Error()), nil
}

func (r *Reconciler) prepareMySQLRestore(ctx context.Context, in *flow) (*Refusal, error) {
	restore := &unstructured.Unstructured{}
	restore.SetGroupVersionKind(mysql.RestoreGVK)
	err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: mysql.RestoreName(Name(in.db.UID))}, restore)
	switch {
	case err == nil:
		in.restore, in.dest = restore, mysql.RestoreDestination(restore)
		return nil, nil
	case !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err):
		return nil, err
	}
	if in.source != nil {
		in.dest = in.source.Destination
		return nil, nil
	}
	destination, err := r.mysqlDestination(ctx, in.cfg, in.db.Spec.Restore.SourceUID)
	switch {
	case errors.Is(err, mysql.ErrNoBackup):
		return refusal(ReasonRestoreSourceEmpty, "the backup prefix %s/ holds no MySQL backup directory", in.db.Spec.Restore.SourceUID), nil
	case s3.IsUnavailable(err):
		return refusal(ReasonBackupStoreFailed, "the object store is not reachable"), nil
	case err != nil:
		return nil, err
	}
	in.dest = destination
	return nil, nil
}

func (r *Reconciler) prepareOperations(ctx context.Context, in *flow) (*Refusal, error) {
	db := *in.db
	in.image = imageFor(db, in.catalog.Version.Spec.Image, Result{})
	if !in.exists || db.Status.Applied == nil {
		return nil, nil
	}
	facts, err := r.operationFacts(ctx, in)
	if err != nil {
		return nil, err
	}
	in.plan = Plan(*db.Status.Applied, db.Spec, facts, db.Annotations)
	in.image = imageFor(db, in.catalog.Version.Spec.Image, in.plan)
	switch {
	case in.plan.Refusal != nil:
		return in.plan.Refusal, nil
	case in.plan.Hold != nil:
		return in.plan.Hold, r.requestUpgradeBackup(ctx, in)
	}
	return nil, nil
}

func (r *Reconciler) requestUpgradeBackup(ctx context.Context, in *flow) error {
	if !in.plan.RequestBackup {
		return nil
	}
	for _, failed := range in.preBackups {
		if err := r.Client.Delete(ctx, failed); client.IgnoreNotFound(err) != nil {
			return err
		}
	}
	return ApplyPlan(ctx, r.Client, in.db.Spec.Engine, r.specOf(in), Result{RequestBackup: true}, r.now())
}

func (r *Reconciler) apply(ctx context.Context, in *flow) (*Refusal, error) {
	db := *in.db
	if err := EnsureEngineSecrets(ctx, r.Client, db, in.project); err != nil {
		return nil, fmt.Errorf("make the engine secrets: %w", err)
	}
	if err := r.applyEngine(ctx, in); err != nil {
		return nil, fmt.Errorf("apply the engine: %w", err)
	}
	vip, err := r.gateAddress(ctx, in)
	switch {
	case errors.Is(err, ErrVIPRangeMissing):
		return refusal(ReasonVIPRangeMissing, "the Setting database.vip-range is empty"), nil
	case errors.Is(err, ErrVIPNameTaken):
		return refusal(ReasonNameTaken, "the Service %s exists and this database does not own it", VIPName(db.UID)), nil
	case errors.Is(err, ErrVIPRangeFull):
		return refusal(ReasonAddressUnavailable, "the gate address range database.vip-range has no free address"), nil
	case err != nil:
		return nil, fmt.Errorf("allocate the gate address: %w", err)
	}
	if err := r.applyGate(ctx, in); err != nil {
		return nil, fmt.Errorf("apply the gate: %w", err)
	}
	return r.applyProxy(ctx, in, vip)
}

func (r *Reconciler) specOf(in *flow) engine.Spec {
	db := *in.db
	labels, _ := OwnedMetadata(db, in.project)
	podLabels := map[string]string{LabelComponent: ComponentEngine, LabelRole: RoleEngine}
	for key, value := range labels {
		podLabels[key] = value
	}
	class := in.catalog.Class.Spec
	return engine.Spec{
		Name:         Name(db.UID),
		BackupID:     string(db.UID),
		Namespace:    PlatformNamespace,
		Version:      db.Spec.Version,
		Image:        in.image,
		CPUMilli:     int(class.CPU.MilliValue()),
		MemoryMi:     int(class.Memory.Value() >> 20),
		StorageGi:    int(db.Spec.StorageGi),
		Instances:    Instances(in.catalog.Class),
		StorageClass: StorageClass,
		Labels:       labels,
		PodLabels:    podLabels,
		Backup:       BackupSpecOf(in.cfg, r.Store, db.Spec.Backup),
		Restore:      restoreOf(in),
		TLSSecret:    in.tlsSecret,
		TLSRevision:  in.tlsRevision,
		Images:       in.catalog.Version.Spec.Companions,
		Suspended:    db.Spec.Suspended,

		RuntimeClassName: in.mysqlRuntime.name,
	}
}

func restoreOf(in *flow) *engine.RestoreSpec {
	restore := RestoreSpecOf(*in.db)
	if restore == nil {
		return restore
	}
	restore.Backup = in.source
	if in.db.Spec.Engine != v1alpha1.DatabaseEngineMySQL {
		return restore
	}
	clusterReady := in.cluster != nil && mysql.Health(in.cluster).Ready
	if !awaitsRestore(*in.db) || in.dest == "" || in.restore == nil && !clusterReady {
		return nil
	}
	restore.Destination = in.dest
	return restore
}

func awaitsRestore(db v1alpha1.Database) bool {
	return db.Spec.Engine == v1alpha1.DatabaseEngineMySQL && db.Spec.Restore != nil && db.Annotations[annotationReadyOnce] != "true"
}

func (r *Reconciler) applyEngine(ctx context.Context, in *flow) error {
	spec := r.specOf(in)
	objects, err := renderFor(in.db.Spec.Engine, spec)
	if err != nil {
		return err
	}
	for _, rendered := range objects {
		obj := Adopt(rendered, *in.db, in.project)
		mysqlCluster := obj.GroupVersionKind() == mysql.ClusterGVK
		var err error
		if mysqlCluster && !in.exists {
			err = r.Client.Create(ctx, obj, client.FieldOwner(v1alpha1.DatabaseFieldManager))
		} else {
			if mysqlCluster {
				obj.SetResourceVersion(in.cluster.GetResourceVersion())
			}
			err = ssa.Apply(ctx, r.Client, obj, v1alpha1.DatabaseFieldManager)
		}
		if meta.IsNoMatchError(err) && obj.GroupVersionKind() == PodMonitorGVK {
			continue
		}
		if err != nil {
			return fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	if err := ApplyPlan(ctx, r.Client, in.db.Spec.Engine, spec, in.plan, r.now()); err != nil {
		return err
	}
	if in.db.Spec.Engine == v1alpha1.DatabaseEnginePostgreSQL {
		if err := r.applyHibernation(ctx, *in.db); err != nil {
			return err
		}
	}
	return markAppliedVersion(ctx, r.Client, in.db)
}

func (r *Reconciler) applyHibernation(ctx context.Context, db v1alpha1.Database) error {
	cluster := &unstructured.Unstructured{}
	cluster.SetGroupVersionKind(ClusterGVK)
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: Name(db.UID)}, cluster); err != nil {
		return client.IgnoreNotFound(err)
	}
	want := "off"
	if db.Spec.Suspended {
		want = "on"
	}
	have := cluster.GetAnnotations()["cnpg.io/hibernation"]
	if have == want || have == "" && want == "off" {
		return nil
	}
	patch := postgres.SuspendPatch(Name(db.UID), db.Spec.Suspended)
	return r.Client.Patch(ctx, cluster, client.RawPatch("application/merge-patch+json", patch.Body), client.FieldOwner(v1alpha1.DatabaseFieldManager))
}

func certificateRevision(ctx context.Context, c client.Reader, key client.ObjectKey) (int64, error) {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(CertificateGVK)
	err := c.Get(ctx, key, certificate)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return CertificateRevision(certificate), nil
}

func (r *Reconciler) applyGate(ctx context.Context, in *flow) error {
	revision, err := certificateRevision(ctx, r.Live, client.ObjectKey{Namespace: PlatformNamespace, Name: ServerCertName(in.db.UID)})
	if err != nil {
		return err
	}
	objects, err := GateObjects(GateInput{Database: *in.db, Project: in.project, Image: in.pods.Image, Port: in.port, Revision: revision})
	if err != nil {
		return err
	}
	for _, obj := range objects {
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.DatabaseFieldManager); err != nil {
			return fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return nil
}

func (r *Reconciler) applyProxy(ctx context.Context, in *flow, vip string) (*Refusal, error) {
	address, err := r.Alloc.EnsureAddress(ctx, r.Client, r.Live, in.db, in.subnet, int32(in.port))
	switch {
	case errors.Is(err, network.ErrNoFreeAddress):
		return refusal(ReasonAddressUnavailable, "the subnet %s has no free address", in.subnet.Name), nil
	case errors.Is(err, ErrEndpointAddressTaken):
		return refusal(ReasonEndpointAddressTaken, "the endpoint address %s is in use in the subnet %s", PinnedAddress(*in.db), in.subnet.Name), nil
	case err != nil:
		return nil, fmt.Errorf("allocate the proxy address: %w", err)
	}
	var existing v1alpha1.SecurityGroup
	switch err := r.Live.Get(ctx, client.ObjectKey{Namespace: in.db.Namespace, Name: SecurityGroupName(in.db.UID)}, &existing); {
	case err == nil && !metav1.IsControlledBy(&existing, in.db):
		return refusal(ReasonNameTaken, "the SecurityGroup %s exists and this database does not own it", existing.Name), nil
	case err != nil && !apierrors.IsNotFound(err):
		return nil, err
	}
	gateSource := vip
	if in.legacy != nil {
		gateSource = ""
	}
	desired := SecurityGroupObject(proxyDatabase(in), in.project, in.subnet, gateSource, in.port)
	if err := ssa.Apply(ctx, r.Client, desired, v1alpha1.DatabaseFieldManager); err != nil {
		return nil, fmt.Errorf("apply the security group: %w", err)
	}
	host, port := customerEndpoint(*in.db, address, int32(in.port))
	if err := EnsureAdminCopy(ctx, r.Client, *in.db, in.project, host, port, in.customerCA); err != nil {
		return nil, fmt.Errorf("copy the admin secret: %w", err)
	}
	var group v1alpha1.SecurityGroup
	if err := r.Live.Get(ctx, client.ObjectKey{Namespace: in.db.Namespace, Name: SecurityGroupName(in.db.UID)}, &group); err != nil {
		return nil, err
	}
	if !meta.IsStatusConditionTrue(group.Status.Conditions, v1alpha1.ConditionReady) {
		return nil, nil
	}
	if in.legacy != nil {
		in.legacyACLReady, err = r.legacySecurityGroupReady(ctx, *in.db, &group, desired)
		if err != nil || !in.legacyACLReady {
			return nil, err
		}
	}
	revision, err := certificateRevision(ctx, r.Live, client.ObjectKey{Namespace: in.db.Namespace, Name: EndpointName(*in.db)})
	if err != nil {
		return nil, err
	}
	objects, err := ProxyObjects(ProxyInput{
		Database: *in.db, Project: in.project, Image: in.pods.Image, Subnet: in.subnet, Address: address, VIP: vip, Port: in.port,
		RuntimeClass: in.pods.RuntimeClass, DNSServers: in.pods.DNSServers, Revision: revision, NIC: in.pods.NIC,
	})
	if err != nil {
		return nil, err
	}
	for _, obj := range objects {
		if conflict, err := r.foreign(ctx, obj, *in.db); err != nil || conflict != nil {
			return conflict, err
		}
		if err := ssa.Apply(ctx, r.Client, obj, v1alpha1.DatabaseFieldManager); err != nil {
			return nil, fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
	}
	return r.applyLegacyFrontend(ctx, in, address)
}

func (r *Reconciler) legacySecurityGroupReady(ctx context.Context, db v1alpha1.Database, group, desired *v1alpha1.SecurityGroup) (bool, error) {
	ready := meta.FindStatusCondition(group.Status.Conditions, v1alpha1.ConditionReady)
	if !ownedByDatabase(group, db) || !group.DeletionTimestamp.IsZero() || !reflect.DeepEqual(group.Spec, desired.Spec) ||
		group.Status.ObservedGeneration != group.Generation || ready == nil || ready.Status != metav1.ConditionTrue || ready.ObservedGeneration != group.Generation {
		return false, nil
	}
	return network.SecurityGroupGenerationProgrammed(ctx, r.Live, group)
}

func (r *Reconciler) foreign(ctx context.Context, desired *unstructured.Unstructured, db v1alpha1.Database) (*Refusal, error) {
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(desired.GroupVersionKind())
	err := r.Live.Get(ctx, client.ObjectKeyFromObject(desired), existing)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if owner := metav1.GetControllerOf(existing); owner == nil || owner.UID != db.UID {
		return refusal(ReasonNameConflict, "the %s %q exists and this database does not own it", desired.GetKind(), desired.GetName()), nil
	}
	return nil, nil
}

func deploymentReady(deployment *unstructured.Unstructured) bool {
	ready, _, _ := unstructured.NestedInt64(deployment.Object, "status", "readyReplicas")
	want, found, _ := unstructured.NestedInt64(deployment.Object, "spec", "replicas")
	if !found {
		want = 1
	}
	return want > 0 && ready >= want
}

func (r *Reconciler) readyDeployment(ctx context.Context, key client.ObjectKey) (bool, error) {
	deployment := &unstructured.Unstructured{}
	deployment.SetGroupVersionKind(DeploymentGVK)
	err := r.Live.Get(ctx, key, deployment)
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	return err == nil && deploymentReady(deployment), err
}

func (r *Reconciler) observe(ctx context.Context, in *flow, refused *Refusal) (Observed, error) {
	db := in.db
	observed := Observed{Refusal: refused, Bootstrapped: Bootstrapped(*db)}
	var err error
	if observed.GateReady, err = r.readyDeployment(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: GateName(db.UID)}); err != nil {
		return observed, err
	}
	if hasLegacyEndpoint(*db) {
		if in.legacyACLReady {
			observed.EndpointReady, err = r.legacyFrontendReady(ctx, in)
		}
	} else {
		observed.EndpointReady, err = r.readyDeployment(ctx, client.ObjectKey{Namespace: db.Namespace, Name: EndpointName(*db)})
	}
	if err != nil {
		return observed, err
	}
	primary := &unstructured.Unstructured{}
	primary.SetGroupVersionKind(primaryGVK(db.Spec.Engine))
	err = r.Live.Get(ctx, client.ObjectKey{Namespace: PlatformNamespace, Name: Name(db.UID)}, primary)
	observed.Stats = db.Status.Stats
	switch {
	case apierrors.IsNotFound(err) || meta.IsNoMatchError(err):
		observed.EngineReason = "the engine objects are not made yet"
	case err != nil:
		return observed, err
	default:
		health := healthFor(db.Spec.Engine, primary)
		if health.Ready && awaitsRestore(*db) {
			health = mysql.RestoreHealth(in.restore)
		}
		observed.EngineReady, observed.EngineReason = health.Ready, health.Reason
		observed.Instances = readyInstances(db.Spec.Engine, primary)
	}
	observed.Upgrading = in.plan.Hold != nil || hasStep(in.plan.Steps, StepUpgrade)
	if refused == nil {
		observed.Image = in.image
	}
	if observed.EngineReady && refused == nil {
		r.settleEngine(ctx, in, &observed)
		r.recordApplied(in, primary, &observed)
	}
	observed.Backup = r.observeBackup(ctx, in, observed.EngineReady && refused == nil)
	return observed, nil
}

func (r *Reconciler) settleEngine(ctx context.Context, in *flow, observed *Observed) {
	access, err := r.Engine(ctx, *in.db)
	if err != nil {
		observed.EngineReason = err.Error()
		return
	}
	if err := EnsureBootstrapped(ctx, r.Client, access.Run, access.Timeout, in.db); err != nil {
		observed.EngineReady, observed.EngineReason = false, engineStepReason(err)
		return
	}
	observed.Bootstrapped = true
	if err := SyncAdmin(ctx, r.Client, access.Run, access.Timeout, *in.db); err != nil {
		observed.EngineReady, observed.EngineReason = false, engineStepReason(err)
		return
	}
	if statsDue(*in.db, r.now()) {
		observed.Stats = SampleStats(ctx, in.db.Spec.Engine, access.Run, in.db.Status.Stats, r.now())
	}
	if address, found := AddressOf(*in.db, in.subnet.Name); found {
		host, port := customerEndpoint(*in.db, address, int32(in.port))
		if err := EnsureAdminCopy(ctx, r.Client, *in.db, in.project, host, port, in.customerCA); err != nil {
			observed.EngineReason = "the admin secret could not be copied"
		}
	}
}

func (r *Reconciler) recordApplied(in *flow, primary *unstructured.Unstructured, observed *Observed) {
	db := *in.db
	if hasStep(in.plan.Steps, StepUpgrade) && reportedImage(db.Spec.Engine, primary) != in.image {
		return
	}
	applied := appliedOf(db)
	applied.Instances = int32(Instances(in.catalog.Class))
	observed.Applied = &applied
	observed.Upgrading = false
}

func engineStepReason(err error) string {
	var rejected *exec.CommandError
	switch {
	case errors.As(err, &rejected):
		return "the engine rejected a setup command"
	case errors.Is(err, exec.ErrNotReady), errors.Is(err, exec.ErrUnavailable), errors.Is(err, exec.ErrBusy):
		return "the engine did not answer a setup command"
	}
	return "a setup step failed"
}

func (r *Reconciler) observeBackup(ctx context.Context, in *flow, ready bool) *v1alpha1.DatabaseBackupStatus {
	db := *in.db
	backups, err := ListBackups(ctx, r.Live, db)
	if err != nil {
		if db.Status.Backup != nil {
			status := *db.Status.Backup
			status.PITRFrom, status.PITRTo = nil, nil
			return &status
		}
		return nil
	}
	request := r.startRequestedBackup(ctx, in, PlanBackupRequest(db, backups, ready, r.now()))
	if !db.Spec.Backup.Enabled {
		if request == nil {
			return nil
		}
		return &v1alpha1.DatabaseBackupStatus{Request: request}
	}
	size, sampledAt := int64(0), (*metav1.Time)(nil)
	if db.Status.Backup != nil {
		size, sampledAt = db.Status.Backup.SizeBytes, db.Status.Backup.SizeSampledAt
	}
	if sampled, at, err := SampleBackupSize(ctx, r.Live, r.Store, in.cfg, db, r.now()); err == nil {
		size, sampledAt = sampled, at
	}
	status := BackupStatusOf(backups, size, sampledAt)
	status.Request = request
	spec := BackupSpecOf(in.cfg, r.Store, db.Spec.Backup)
	status.PITRFrom, status.PITRTo = nativeRecoveryWindow(ctx, r.Live, db, in.project, spec, status.LastAt, r.now())
	return status
}

func (r *Reconciler) startRequestedBackup(ctx context.Context, in *flow, plan BackupRequestPlan) *v1alpha1.DatabaseBackupRequest {
	if plan.Create == "" {
		return plan.Status
	}
	backup, err := PreUpgradeBackup(in.db.Spec.Engine, r.specOf(in), plan.Create)
	if err != nil {
		return plan.Status
	}
	backup.SetAnnotations(map[string]string{annotationBackupRequestedAt: plan.Status.At.UTC().Format(time.RFC3339)})
	if err := client.IgnoreAlreadyExists(r.Client.Create(ctx, backup, client.FieldOwner(v1alpha1.DatabaseFieldManager))); err != nil {
		return plan.Status
	}
	started := *plan.Status
	started.Phase = v1alpha1.DatabaseBackupRequestRunning
	return &started
}

func statsDue(db v1alpha1.Database, now time.Time) bool {
	return StatsRequeueIn(db.Status.Stats, now) == 0
}

func requeueFor(verdict Verdict) time.Duration {
	if verdict.Phase == v1alpha1.DatabasePhaseReady || verdict.Phase == v1alpha1.DatabasePhaseSuspended {
		return readyRequeue
	}
	return progressRequeue
}
