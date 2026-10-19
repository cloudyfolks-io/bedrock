package mysql

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
)

const (
	Port        = 3306
	CRVersion   = "1.2.0"
	StorageName = "rgw"

	ImageBackup  = "backup"
	ImageHAProxy = "haproxy"
	ImageInit    = "init"
	ImageToolkit = "toolkit"

	componentLabel   = "app.kubernetes.io/component"
	rootPasswordFile = "/etc/mysql/mysql-users-secret/root"
	socketPath       = "/var/lib/mysql/mysql.sock"
	readyState       = "ready"
	restartedAtPath  = `{"spec":{"mysql":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}`
)

var (
	ClusterGVK = schema.GroupVersionKind{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQL"}
	BackupGVK  = schema.GroupVersionKind{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQLBackup"}
	RestoreGVK = schema.GroupVersionKind{Group: "ps.percona.com", Version: "v1", Kind: "PerconaServerMySQLRestore"}

	ErrTargetTimeUnsupported = errors.New("a MySQL restore has no target time")
	ErrDestinationMissing    = errors.New("a MySQL restore needs the destination of a backup")
)

func GVKs() []schema.GroupVersionKind {
	return []schema.GroupVersionKind{ClusterGVK, BackupGVK, RestoreGVK}
}

func MaxConnections(memoryMi int) int {
	switch {
	case memoryMi <= 512:
		return 60
	case memoryMi <= 2048:
		return 150
	case memoryMi > 4096:
		return 400
	default:
		return 250
	}
}

func configuration(memoryMi int) string {
	return strings.Join([]string{
		"[mysqld]",
		"character_set_server=utf8mb4",
		"collation_server=utf8mb4_0900_ai_ci",
		"default_time_zone=+00:00",
		"skip_name_resolve=ON",
		"partial_revokes=ON",
		fmt.Sprintf("innodb_buffer_pool_size=%dM", memoryMi*55/100),
		fmt.Sprintf("max_connections=%d", MaxConnections(memoryMi)),
		"",
	}, "\n")
}

func checkSpec(s engine.Spec) error {
	if s.Instances != 1 && s.Instances != 3 {
		return fmt.Errorf("instances must be 1 or 3")
	}
	if s.CPUMilli <= 0 || s.MemoryMi <= 0 || s.StorageGi <= 0 {
		return fmt.Errorf("cpu, memory and storage must be greater than zero")
	}
	return nil
}

func checkRestore(s engine.Spec) error {
	if s.Restore == nil {
		return nil
	}
	if s.Restore.TargetTime != nil {
		return ErrTargetTimeUnsupported
	}
	if s.Restore.Destination == "" {
		return ErrDestinationMissing
	}
	return nil
}

func s3Block(b engine.BackupSpec, prefix string) map[string]any {
	s3 := map[string]any{"bucket": b.Bucket, "credentialsSecret": b.SecretName}
	if prefix != "" {
		s3["prefix"] = prefix
	}
	if b.Region != "" {
		s3["region"] = b.Region
	}
	if b.Endpoint != "" {
		s3["endpointUrl"] = b.Endpoint
	}
	return s3
}

func podLabels(s engine.Spec) map[string]any {
	labels := map[string]any{}
	for key, value := range s.PodLabels {
		if key != componentLabel {
			labels[key] = value
		}
	}
	return labels
}

func volumeClaim(s engine.Spec) map[string]any {
	claim := map[string]any{
		"accessModes": []any{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", s.StorageGi)}},
	}
	if s.StorageClass != "" {
		claim["storageClassName"] = s.StorageClass
	}
	return claim
}

func mysqlBlock(s engine.Spec) map[string]any {
	block := map[string]any{
		"clusterType":   "group-replication",
		"size":          int64(s.Instances),
		"image":         s.Image,
		"resources":     engine.ResourceBlock(s.CPUMilli, s.MemoryMi),
		"volumeSpec":    map[string]any{"persistentVolumeClaim": volumeClaim(s)},
		"configuration": configuration(s.MemoryMi),
	}
	if labels := podLabels(s); len(labels) > 0 {
		block["labels"] = labels
	}
	if image := s.Images[ImageInit]; image != "" {
		block["initImage"] = image
	}
	return block
}

func proxyBlock(s engine.Spec) map[string]any {
	haproxy := map[string]any{"enabled": true, "size": int64(3), "labels": podLabels(s)}
	if image := s.Images[ImageHAProxy]; image != "" {
		haproxy["image"] = image
	}
	return map[string]any{"haproxy": haproxy, "router": map[string]any{"enabled": false}}
}

func backupBlock(s engine.Spec) map[string]any {
	backup := map[string]any{
		"enabled": true,
		"storages": map[string]any{StorageName: map[string]any{
			"type": "s3",
			"s3":   s3Block(s.Backup, s.Name),
		}},
		"schedule": []any{map[string]any{
			"name": "daily", "schedule": "0 2 * * *", "keep": int64(s.Backup.RetentionDays), "storageName": StorageName,
		}},
	}
	if image := s.Images[ImageBackup]; image != "" {
		backup["image"] = image
	}
	return backup
}

func clusterSpec(s engine.Spec) map[string]any {
	spec := map[string]any{
		"crVersion":   CRVersion,
		"secretsName": engine.Named(s.Name, "superuser"),
		"mysql":       mysqlBlock(s),
	}
	if s.Instances == 1 {
		spec["unsafeFlags"] = map[string]any{"mysqlSize": true, "proxy": true}
		spec["proxy"] = map[string]any{"haproxy": map[string]any{"enabled": false}, "router": map[string]any{"enabled": false}}
	} else {
		spec["proxy"] = proxyBlock(s)
	}
	if s.Suspended {
		spec["pause"] = true
	}
	if image := s.Images[ImageToolkit]; image != "" {
		spec["toolkit"] = map[string]any{"image": image}
	}
	if s.Backup.Enabled {
		spec["backup"] = backupBlock(s)
	}
	return spec
}

func restoreObject(s engine.Spec) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ps.percona.com/v1", "kind": RestoreGVK.Kind,
		"metadata": map[string]any{"name": engine.Named(s.Name, "restore"), "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec": map[string]any{
			"clusterName": s.Name,
			"backupSource": map[string]any{
				"destination": s.Restore.Destination,
				"storage":     map[string]any{"type": "s3", "s3": s3Block(s.Backup, "")},
			},
		},
	}}
}

func Render(s engine.Spec) ([]*unstructured.Unstructured, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	if err := checkRestore(s); err != nil {
		return nil, err
	}
	out := []*unstructured.Unstructured{{Object: map[string]any{
		"apiVersion": "ps.percona.com/v1", "kind": ClusterGVK.Kind,
		"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec":     clusterSpec(s),
	}}}
	if s.Restore != nil {
		out = append(out, restoreObject(s))
	}
	return append(out, engine.DBService(s, Port, ServiceSelector(s.Name))), nil
}

func ServiceSelector(name string) map[string]string {
	return map[string]string{
		"app.kubernetes.io/instance": name, componentLabel: "mysql", "mysql.percona.com/primary": "true",
	}
}

func patchSpec(s engine.Spec) map[string]any {
	spec := map[string]any{"mysql": map[string]any{
		"size":          s.Instances,
		"image":         s.Image,
		"resources":     engine.ResourceBlock(s.CPUMilli, s.MemoryMi),
		"configuration": configuration(s.MemoryMi),
		"volumeSpec": map[string]any{"persistentVolumeClaim": map[string]any{
			"resources": map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", s.StorageGi)}},
		}},
	}}
	if s.Instances == 1 {
		spec["unsafeFlags"] = map[string]any{"mysqlSize": true, "proxy": true}
		spec["proxy"] = map[string]any{"haproxy": map[string]any{"enabled": false}}
	} else {
		spec["unsafeFlags"] = nil
		spec["proxy"] = map[string]any{"haproxy": map[string]any{"enabled": true, "size": 3}}
	}
	if image := s.Images[ImageBackup]; image != "" && s.Backup.Enabled {
		spec["backup"] = map[string]any{"image": image}
	}
	return spec
}

func Patches(s engine.Spec) ([]engine.Patch, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(map[string]any{"spec": patchSpec(s)})
	if err != nil {
		return nil, err
	}
	return []engine.Patch{{GVK: ClusterGVK, Name: s.Name, Body: raw}}, nil
}

func SuspendPatch(name string, on bool) engine.Patch {
	return engine.Patch{GVK: ClusterGVK, Name: name, Body: []byte(`{"spec":{"pause":` + strconv.FormatBool(on) + `}}`)}
}

func RestartPatch(name, now string) engine.Patch {
	return engine.Patch{GVK: ClusterGVK, Name: name, Body: []byte(fmt.Sprintf(restartedAtPath, now))}
}

func Health(obj *unstructured.Unstructured) engine.Health {
	state, _, _ := unstructured.NestedString(obj.Object, "status", "state")
	if state == readyState {
		return engine.Health{Ready: true}
	}
	if state == "" {
		state = "waiting for the operator"
	}
	return engine.Health{Reason: state}
}

func Target(namespace, name string) exec.Target {
	script := `MYSQL_PWD="$(cat ` + rootPasswordFile + `)" exec env LC_ALL=C mysql --no-defaults --socket=` + socketPath + ` -uroot --batch --raw --skip-column-names --default-character-set=utf8mb4`
	return exec.Target{
		Namespace: namespace,
		Selector:  client.MatchingLabels{"app.kubernetes.io/instance": name, "app.kubernetes.io/component": "mysql", "mysql.percona.com/primary": "true"},
		Container: "mysql",
		Command:   []string{"sh", "-c", script},
		Classify:  func(stderr string) exec.Kind { return engine.KindOf(Classify(stderr)) },
	}
}
