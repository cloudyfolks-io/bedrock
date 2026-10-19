package engine

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	AdminUser       = "dbadmin"
	DefaultDatabase = "app"
)

type Spec struct {
	Name         string
	Namespace    string
	Version      string
	Image        string
	CPUMilli     int
	MemoryMi     int
	StorageGi    int
	Instances    int
	StorageClass string
	Labels       map[string]string
	PodLabels    map[string]string
	Images       map[string]string
	Backup       BackupSpec
	Restore      *RestoreSpec
	Suspended    bool
}

type BackupSpec struct {
	Enabled       bool
	Bucket        string
	Endpoint      string
	Region        string
	RetentionDays int
	SecretName    string
}

type RestoreSpec struct {
	SourceUID   string
	TargetTime  *time.Time
	Destination string
}

type Health struct {
	Ready  bool
	Reason string
}

type Patch struct {
	GVK       schema.GroupVersionKind
	Namespace string
	Name      string
	Body      []byte
}

func Named(name, suffix string) string {
	if suffix == "" {
		return name
	}
	return name + "-" + suffix
}

func MemoryString(mi int) string { return fmt.Sprintf("%dMi", mi) }

func CPUString(milli int) string { return fmt.Sprintf("%dm", milli) }

func ResourceBlock(cpuMilli, memoryMi int) map[string]any {
	return map[string]any{
		"requests": map[string]any{"cpu": CPUString(cpuMilli), "memory": MemoryString(memoryMi)},
		"limits":   map[string]any{"cpu": CPUString(cpuMilli), "memory": MemoryString(memoryMi)},
	}
}

func Labels(in map[string]string) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func DBService(s Spec, port int, selector map[string]string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"name": Named(s.Name, "db"), "namespace": s.Namespace, "labels": Labels(s.Labels)},
		"spec": map[string]any{
			"type":     "ClusterIP",
			"selector": Labels(selector),
			"ports":    []any{map[string]any{"name": "db", "port": int64(port), "targetPort": int64(port), "protocol": "TCP"}},
		},
	}}
}
