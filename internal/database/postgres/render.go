package postgres

import (
	"encoding/json"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
)

const (
	PluginName = "barman-cloud.cloudnative-pg.io"
	Port       = 5432
)

var (
	ClusterGVK         = schema.GroupVersionKind{Group: "postgresql.cnpg.io", Version: "v1", Kind: "Cluster"}
	healthyPhase       = "Cluster in healthy state"
	hibernationOn      = `{"metadata":{"annotations":{"cnpg.io/hibernation":"on"}}}`
	hibernationOff     = `{"metadata":{"annotations":{"cnpg.io/hibernation":"off"}}}`
	restartedAtPattern = `{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}`
)

func MaxConnections(memoryMi int) int {
	switch {
	case memoryMi <= 512:
		return 50
	case memoryMi <= 1024:
		return 100
	case memoryMi <= 2048:
		return 200
	case memoryMi <= 4096:
		return 300
	default:
		return 500
	}
}

func parameters(memoryMi int) map[string]any {
	return map[string]any{
		"shared_buffers":          fmt.Sprintf("%dMB", memoryMi/4),
		"effective_cache_size":    fmt.Sprintf("%dMB", memoryMi*60/100),
		"max_connections":         strconv.Itoa(MaxConnections(memoryMi)),
		"log_min_error_statement": "panic",
		"password_encryption":     "scram-sha-256",
		"timezone":                "UTC",
		"log_timezone":            "UTC",
	}
}

func checkSpec(s engine.Spec) error {
	if s.Instances != 1 && s.Instances != 3 {
		return fmt.Errorf("instances must be 1 or 3")
	}
	if s.CPUMilli <= 0 || s.MemoryMi <= 0 || s.StorageGi <= 0 {
		return fmt.Errorf("cpu, memory and storage must be greater than zero")
	}
	if r := s.Restore; r != nil && r.Backup != nil && r.Backup.Postgres != nil {
		if r.SourceUID == "" || r.Backup.Prefix != r.SourceUID || (r.Backup.ServerName != r.SourceUID && r.Backup.ServerName != "db-"+r.SourceUID) || r.Backup.Destination != "" || r.Backup.Bucket == "" || r.Backup.SecretName == "" {
			return fmt.Errorf("the selected PostgreSQL backup must belong to the imported source")
		}
		return r.Backup.Postgres.Validate(r.TargetTime)
	}
	return nil
}

func bootstrap(s engine.Spec) map[string]any {
	app := map[string]any{"name": engine.Named(s.Name, "app")}
	if s.Restore == nil {
		return map[string]any{"initdb": map[string]any{
			"database":      engine.DefaultDatabase,
			"owner":         engine.AdminUser,
			"secret":        app,
			"encoding":      "UTF8",
			"localeCollate": "C.UTF-8",
			"localeCType":   "C.UTF-8",
		}}
	}
	recovery := map[string]any{"source": "source", "database": engine.DefaultDatabase, "owner": engine.AdminUser, "secret": app}
	if target := recoveryTarget(s.Restore); len(target) > 0 {
		recovery["recoveryTarget"] = target
	}
	return map[string]any{"recovery": recovery}
}

func recoveryTarget(restore *engine.RestoreSpec) map[string]any {
	target := map[string]any{}
	if restore.Backup != nil && restore.Backup.Postgres != nil {
		target["backupID"] = restore.Backup.Postgres.BackupID
		if restore.TargetTime == nil {
			target["targetImmediate"] = true
		}
	}
	if restore.TargetTime != nil {
		target["targetTime"] = restore.TargetTime.UTC().Format("2006-01-02 15:04:05.000000+00")
	}
	return target
}

func externalClusters(s engine.Spec) []any {
	_, _, server := s.Restore.BackupSource(s.Backup)
	return []any{map[string]any{
		"name": "source",
		"plugin": map[string]any{
			"name": PluginName,
			"parameters": map[string]any{
				"barmanObjectName": engine.Named(s.Name, "source"),
				"serverName":       server,
			},
		},
	}}
}

func Render(s engine.Spec) ([]*unstructured.Unstructured, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	spec := map[string]any{
		"instances":             int64(s.Instances),
		"imageName":             s.Image,
		"enableSuperuserAccess": true,
		"superuserSecret":       map[string]any{"name": engine.Named(s.Name, "superuser")},
		"inheritedMetadata":     map[string]any{"labels": engine.Labels(s.PodLabels)},
		"storage":               map[string]any{"size": fmt.Sprintf("%dGi", s.StorageGi)},
		"resources":             engine.ResourceBlock(s.CPUMilli, s.MemoryMi),
		"monitoring":            map[string]any{"enablePodMonitor": true},
		"postgresql": map[string]any{
			"parameters": parameters(s.MemoryMi),
			"pg_hba":     []any{"host all postgres all reject"},
		},
		"bootstrap": bootstrap(s),
	}
	if s.StorageClass != "" {
		spec["storage"].(map[string]any)["storageClass"] = s.StorageClass
	}
	if s.TLSSecret != "" {
		spec["certificates"] = map[string]any{"serverTLSSecret": s.TLSSecret, "serverCASecret": s.TLSSecret}
		spec["postgresql"].(map[string]any)["pg_hba"] = []any{
			"host all postgres all reject", "hostssl all all all scram-sha-256", "host all all all reject",
		}
	}
	out := []*unstructured.Unstructured{}
	if s.Backup.Enabled {
		spec["plugins"] = []any{map[string]any{
			"name": PluginName, "isWALArchiver": true,
			"parameters": map[string]any{"barmanObjectName": s.Name, "serverName": s.BackupID},
		}}
		out = append(out, objectStore(s, s.Name, s.BackupID))
	}
	if s.Restore != nil {
		spec["externalClusters"] = externalClusters(s)
		backup, prefix, _ := s.Restore.BackupSource(s.Backup)
		source := s
		source.Backup = backup
		out = append(out, objectStore(source, engine.Named(s.Name, "source"), prefix))
	}
	out = append(out, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "Cluster",
		"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec":     spec,
	}})
	if s.Backup.Enabled {
		out = append(out, scheduledBackup(s))
	}
	out = append(out, engine.DBService(s, Port, map[string]string{"cnpg.io/cluster": s.Name, "cnpg.io/instanceRole": "primary"}))
	return out, nil
}

func scheduledBackup(s engine.Spec) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1", "kind": "ScheduledBackup",
		"metadata": map[string]any{"name": engine.Named(s.Name, "daily"), "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec": map[string]any{
			"schedule": "0 0 2 * * *", "backupOwnerReference": "self", "method": "plugin",
			"cluster":             map[string]any{"name": s.Name},
			"pluginConfiguration": map[string]any{"name": PluginName},
		},
	}}
}

func objectStore(s engine.Spec, name, prefix string) *unstructured.Unstructured {
	spec := map[string]any{
		"instanceSidecarConfiguration": map[string]any{"resources": sidecarResources()},
		"configuration": map[string]any{
			"destinationPath": fmt.Sprintf("s3://%s/%s/", s.Backup.Bucket, prefix),
			"endpointURL":     s.Backup.Endpoint,
			"s3Credentials": map[string]any{
				"accessKeyId":     map[string]any{"name": s.Backup.SecretName, "key": "ACCESS_KEY_ID"},
				"secretAccessKey": map[string]any{"name": s.Backup.SecretName, "key": "ACCESS_SECRET_KEY"},
			},
		},
	}
	if s.Backup.RetentionDays > 0 {
		spec["retentionPolicy"] = fmt.Sprintf("%dd", s.Backup.RetentionDays)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "barmancloud.cnpg.io/v1", "kind": "ObjectStore",
		"metadata": map[string]any{"name": name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec":     spec,
	}}
}

func sidecarResources() map[string]any {
	return map[string]any{"requests": map[string]any{"cpu": "50m", "memory": "64Mi"}}
}

func Patches(s engine.Spec) ([]engine.Patch, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	body := map[string]any{"spec": map[string]any{
		"instances":  s.Instances,
		"imageName":  s.Image,
		"resources":  engine.ResourceBlock(s.CPUMilli, s.MemoryMi),
		"storage":    map[string]any{"size": fmt.Sprintf("%dGi", s.StorageGi)},
		"postgresql": map[string]any{"parameters": parameters(s.MemoryMi)},
	}}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return []engine.Patch{{GVK: ClusterGVK, Name: s.Name, Body: raw}}, nil
}

func SuspendPatch(name string, on bool) engine.Patch {
	body := hibernationOff
	if on {
		body = hibernationOn
	}
	return engine.Patch{GVK: ClusterGVK, Name: name, Body: []byte(body)}
}

func RestartPatch(name, now string) engine.Patch {
	return engine.Patch{GVK: ClusterGVK, Name: name, Body: []byte(fmt.Sprintf(restartedAtPattern, now))}
}

func Health(obj *unstructured.Unstructured) engine.Health {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyInstances")
	want, _, _ := unstructured.NestedInt64(obj.Object, "spec", "instances")
	if phase == healthyPhase && ready >= want && want > 0 {
		return engine.Health{Ready: true}
	}
	if phase == "" {
		phase = "waiting for the operator"
	}
	return engine.Health{Reason: phase}
}
