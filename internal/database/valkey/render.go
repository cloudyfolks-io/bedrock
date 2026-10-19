package valkey

import (
	"encoding/json"
	"fmt"
	"strconv"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/database/engine"
	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
)

const (
	Port         = 6379
	MetricsPort  = 9121
	PlatformUser = "_platform"
	ExporterUser = "_exporter"

	ImageExporter = "exporter"

	componentLabel = "app.kubernetes.io/component"
	instanceLabel  = "app.kubernetes.io/instance"
	engineValue    = "engine"
	runAsID        = int64(999)
)

const initScript = `set -eu
if [ ! -f /data/users.acl ]; then
  platform="$(printf %s "$PLATFORM_PASSWORD" | sha256sum | cut -d' ' -f1)"
  admin="$(printf %s "$ADMIN_PASSWORD" | sha256sum | cut -d' ' -f1)"
  exporter="$(printf %s "$EXPORTER_PASSWORD" | sha256sum | cut -d' ' -f1)"
  umask 077
  printf 'user default off\nuser _platform on #%s ~* &* +@all\nuser dbadmin on #%s ~* &* +@all -@admin -@dangerous\nuser _exporter on #%s ~* &* -@all +info +ping +client|list +slowlog|get +slowlog|len +latency|latest +latency|history +dbsize\n' "$platform" "$admin" "$exporter" > /data/users.acl.tmp
  mv /data/users.acl.tmp /data/users.acl
fi
`

var (
	DeploymentGVK = schema.GroupVersionKind{Group: "apps", Version: "v1", Kind: "Deployment"}
	ClaimGVK      = schema.GroupVersionKind{Version: "v1", Kind: "PersistentVolumeClaim"}
	PodMonitorGVK = schema.GroupVersionKind{Group: "monitoring.coreos.com", Version: "v1", Kind: "PodMonitor"}

	renamedCommands = []string{"CONFIG", "DEBUG", "MODULE", "SHUTDOWN", "REPLICAOF", "SLAVEOF"}
)

func checkSpec(s engine.Spec) error {
	if s.Instances != 1 {
		return fmt.Errorf("valkey runs as a single instance")
	}
	if s.CPUMilli <= 0 || s.MemoryMi <= 0 || s.StorageGi <= 0 {
		return fmt.Errorf("cpu, memory and storage must be greater than zero")
	}
	if s.Images[ImageExporter] == "" {
		return fmt.Errorf("the exporter image is missing")
	}
	if s.Restore != nil {
		return fmt.Errorf("valkey has no restore")
	}
	return nil
}

func Selector(name string) map[string]string {
	return map[string]string{instanceLabel: name, componentLabel: engineValue}
}

func secretEnv(name, secret, key string) map[string]any {
	return map[string]any{"name": name, "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": secret, "key": key}}}
}

func securityContext() map[string]any {
	return map[string]any{
		"allowPrivilegeEscalation": false, "runAsNonRoot": true, "runAsUser": runAsID, "runAsGroup": runAsID,
		"capabilities": map[string]any{"drop": []any{"ALL"}}, "seccompProfile": map[string]any{"type": "RuntimeDefault"},
	}
}

func execProbe(initialDelay int64) map[string]any {
	command := []any{"valkey-cli", "-e", "--no-auth-warning", "--user", PlatformUser, "ping"}
	return map[string]any{
		"exec": map[string]any{"command": command}, "initialDelaySeconds": initialDelay,
		"periodSeconds": int64(10), "timeoutSeconds": int64(5), "failureThreshold": int64(3),
	}
}

func serverArguments(s engine.Spec) []any {
	arguments := []any{
		"valkey-server", "--bind", "0.0.0.0", "--protected-mode", "no",
		"--aclfile", "/data/users.acl", "--dir", "/data",
		"--appendonly", "yes", "--appendfsync", "everysec",
		"--maxmemory", fmt.Sprintf("%dmb", s.MemoryMi*75/100), "--maxmemory-policy", "noeviction",
	}
	for _, command := range renamedCommands {
		arguments = append(arguments, "--rename-command", command, "")
	}
	return arguments
}

func aclInitContainer(s engine.Spec, mount []any) map[string]any {
	return map[string]any{
		"name": "acl", "image": s.Image, "command": []any{"sh", "-c", initScript},
		"env": []any{
			secretEnv("PLATFORM_PASSWORD", engine.Named(s.Name, "superuser"), "password"),
			secretEnv("ADMIN_PASSWORD", engine.Named(s.Name, "app"), "password"),
			secretEnv("EXPORTER_PASSWORD", engine.Named(s.Name, "superuser"), "exporter-password"),
		},
		"volumeMounts": mount, "securityContext": securityContext(),
		"resources": engine.ResourceBlock(100, 64),
	}
}

func serverContainer(s engine.Spec, mount []any) map[string]any {
	return map[string]any{
		"name": "valkey", "image": s.Image,
		"args":            serverArguments(s),
		"ports":           []any{map[string]any{"name": "db", "containerPort": int64(Port)}},
		"env":             []any{secretEnv("REDISCLI_AUTH", engine.Named(s.Name, "superuser"), "password")},
		"resources":       engine.ResourceBlock(s.CPUMilli, s.MemoryMi),
		"volumeMounts":    mount,
		"securityContext": securityContext(),
		"readinessProbe":  execProbe(5),
		"livenessProbe":   execProbe(30),
	}
}

func exporterContainer(s engine.Spec) map[string]any {
	sidecarSecurity := securityContext()
	sidecarSecurity["readOnlyRootFilesystem"] = true
	return map[string]any{
		"name": "exporter", "image": s.Images[ImageExporter],
		"ports": []any{map[string]any{"name": "metrics", "containerPort": int64(MetricsPort)}},
		"env": []any{
			map[string]any{"name": "REDIS_ADDR", "value": "redis://127.0.0.1:" + strconv.Itoa(Port)},
			map[string]any{"name": "REDIS_USER", "value": ExporterUser},
			secretEnv("REDIS_PASSWORD", engine.Named(s.Name, "superuser"), "exporter-password"),
		},
		"resources":       engine.ResourceBlock(50, 64),
		"securityContext": sidecarSecurity,
	}
}

func podSpec(s engine.Spec) map[string]any {
	mount := []any{map[string]any{"name": "data", "mountPath": "/data", "subPath": "data"}}
	return map[string]any{
		"automountServiceAccountToken": false,
		"securityContext":              map[string]any{"fsGroup": runAsID},
		"initContainers":               []any{aclInitContainer(s, mount)},
		"containers":                   []any{serverContainer(s, mount), exporterContainer(s)},
		"volumes":                      []any{map[string]any{"name": "data", "persistentVolumeClaim": map[string]any{"claimName": engine.Named(s.Name, "data")}}},
	}
}

func claim(s engine.Spec) *unstructured.Unstructured {
	spec := map[string]any{
		"accessModes": []any{"ReadWriteOnce"},
		"resources":   map[string]any{"requests": map[string]any{"storage": fmt.Sprintf("%dGi", s.StorageGi)}},
	}
	if s.StorageClass != "" {
		spec["storageClassName"] = s.StorageClass
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "PersistentVolumeClaim",
		"metadata": map[string]any{"name": engine.Named(s.Name, "data"), "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec":     spec,
	}}
}

func deployment(s engine.Spec) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "Deployment",
		"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec": map[string]any{
			"replicas": int64(1), "strategy": map[string]any{"type": "Recreate"},
			"selector": map[string]any{"matchLabels": engine.Labels(Selector(s.Name))},
			"template": map[string]any{
				"metadata": map[string]any{"labels": engine.Labels(s.PodLabels)},
				"spec":     podSpec(s),
			},
		},
	}}
}

func podMonitor(s engine.Spec) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "monitoring.coreos.com/v1", "kind": "PodMonitor",
		"metadata": map[string]any{"name": s.Name, "namespace": s.Namespace, "labels": engine.Labels(s.Labels)},
		"spec": map[string]any{
			"selector":            map[string]any{"matchLabels": engine.Labels(Selector(s.Name))},
			"podMetricsEndpoints": []any{map[string]any{"port": "metrics", "interval": "30s"}},
		},
	}}
}

func Render(s engine.Spec) ([]*unstructured.Unstructured, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	return []*unstructured.Unstructured{
		claim(s), deployment(s), engine.DBService(s, Port, Selector(s.Name)), podMonitor(s),
	}, nil
}

func Patches(s engine.Spec) ([]engine.Patch, error) {
	if err := checkSpec(s); err != nil {
		return nil, err
	}
	deploymentBody, err := json.Marshal(map[string]any{"spec": map[string]any{"template": map[string]any{"spec": podSpec(s)}}})
	if err != nil {
		return nil, err
	}
	claimBody := []byte(fmt.Sprintf(`{"spec":{"resources":{"requests":{"storage":"%dGi"}}}}`, s.StorageGi))
	return []engine.Patch{
		{GVK: DeploymentGVK, Name: s.Name, Body: deploymentBody},
		{GVK: ClaimGVK, Name: engine.Named(s.Name, "data"), Body: claimBody},
	}, nil
}

func SuspendPatch(name string, on bool) engine.Patch {
	replicas := 1
	if on {
		replicas = 0
	}
	return engine.Patch{GVK: DeploymentGVK, Name: name, Body: []byte(`{"spec":{"replicas":` + strconv.Itoa(replicas) + `}}`)}
}

func RestartPatch(name, now string) engine.Patch {
	body := fmt.Sprintf(`{"spec":{"template":{"metadata":{"annotations":{"kubectl.kubernetes.io/restartedAt":%q}}}}}`, now)
	return engine.Patch{GVK: DeploymentGVK, Name: name, Body: []byte(body)}
}

func Health(obj *unstructured.Unstructured) engine.Health {
	ready, _, _ := unstructured.NestedInt64(obj.Object, "status", "readyReplicas")
	want, found, _ := unstructured.NestedInt64(obj.Object, "spec", "replicas")
	if !found {
		want = 1
	}
	if want > 0 && ready >= want {
		return engine.Health{Ready: true}
	}
	return engine.Health{Reason: "waiting for the pod"}
}

func Target(namespace, name string) exec.Target {
	return exec.Target{
		Namespace: namespace,
		Selector:  client.MatchingLabels(Selector(name)),
		Container: "valkey",
		Command:   []string{"valkey-cli", "-e", "--no-auth-warning", "--user", PlatformUser},
		Classify:  func(stderr string) exec.Kind { return engine.KindOf(Classify(stderr)) },
	}
}
