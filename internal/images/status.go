package images

import (
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	ConditionAdditionalCA           = "AdditionalCAReady"
	ReasonForeignConfigMap          = "ForeignConfigMap"
	ReasonImported                  = "Imported"
	ReasonImportScheduled           = "ImportScheduled"
	ReasonWaitingForUpload          = "WaitingForUpload"
	ReasonImporting                 = "Importing"
	ReasonSnapshotting              = "Snapshotting"
	ReasonWaitingForDataSource      = "WaitingForDataSource"
	ReasonSourceChanged             = "SourceChanged"
	ReasonVolumeNotFound            = "VolumeNotFound"
	ReasonClaimNotFound             = "ClaimNotFound"
	ReasonPlatformHostNotSet        = "PlatformHostNotSet"
	ReasonSnapshotFormatUnavailable = "SnapshotFormatUnavailable"
	ReasonCatalogNamespaceMissing   = "CatalogNamespaceMissing"
	ReasonUploadNotSupported        = "UploadNotSupported"
	ReasonSnapshotFailed            = "SnapshotFailed"
	ReasonApplyFailed               = "ApplyFailed"
	ReasonImportFailed              = "ImportFailed"
)

type Result struct {
	Phase      string
	Reason     string
	Message    string
	Progress   string
	Digest     string
	SizeBytes  int64
	DataSource string
	UploadURL  string
	Versions   []v1alpha1.ImageVersion

	AdditionalCAConflict string
}

var importingPhases = []string{
	"ImportInProgress", "CloneInProgress", "SnapshotForSmartCloneInProgress", "SmartClonePVCInProgress",
	"CloneFromSnapshotSourceInProgress", "CSICloneInProgress", "ExpansionInProgress", "NamespaceTransferInProgress",
	"PrepClaimInProgress", "RebindInProgress",
}

func PhaseOfDataVolume(dv *unstructured.Unstructured) Result {
	_, upload, _ := unstructured.NestedMap(dv.Object, "spec", "source", "upload")
	phase, _, _ := unstructured.NestedString(dv.Object, "status", "phase")
	result := Result{
		Phase:    v1alpha1.ImagePhasePending,
		Reason:   ReasonImportScheduled,
		Message:  firstFalseMessage(dv, "Running", "Bound", "Ready"),
		Progress: percentOf(dv),
	}
	switch {
	case phase == "Failed":
		result.Phase, result.Reason = v1alpha1.ImagePhaseFailed, ReasonImportFailed
	case phase == "UploadReady" && upload:
		result.Phase, result.Reason = v1alpha1.ImagePhaseWaitingForUpload, ReasonWaitingForUpload
	case phase == "Succeeded":
		result.Phase, result.Reason = v1alpha1.ImagePhaseImporting, ReasonSnapshotting
	case slices.Contains(importingPhases, phase):
		result.Phase, result.Reason = v1alpha1.ImagePhaseImporting, ReasonImporting
	}
	return result
}

func PhaseOfCron(cron, alias, snapshot *unstructured.Unstructured) Result {
	if cron == nil {
		return Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonImportScheduled}
	}
	snapshotReady := snapshot != nil && SnapshotReady(snapshot)
	aliasReady := alias != nil && conditionStatus(alias, "Ready") == "True"
	format, _, _ := unstructured.NestedString(cron.Object, "status", "sourceFormat")
	switch {
	case conditionStatus(cron, "UpToDate") == "True" && snapshotReady && aliasReady:
		return Result{Phase: v1alpha1.ImagePhaseReady, Reason: ReasonImported}
	case snapshotReady && !aliasReady:
		return Result{Phase: v1alpha1.ImagePhaseImporting, Reason: ReasonWaitingForDataSource}
	case conditionStatus(cron, "Progressing") == "True":
		return Result{Phase: v1alpha1.ImagePhaseImporting, Reason: ReasonImporting}
	case format == "pvc":
		return Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonSnapshotFormatUnavailable}
	default:
		return Result{Phase: v1alpha1.ImagePhasePending, Reason: ReasonImportScheduled, Message: conditionMessage(cron, "UpToDate")}
	}
}

func CronImport(cron *unstructured.Unstructured) (dataVolume, digest string, ok bool) {
	imports, _, _ := unstructured.NestedSlice(cron.Object, "status", "currentImports")
	if len(imports) == 0 {
		return "", "", false
	}
	first, isMap := imports[0].(map[string]any)
	if !isMap {
		return "", "", false
	}
	dataVolume, _ = first["DataVolumeName"].(string)
	digest, _ = first["Digest"].(string)
	return dataVolume, digest, dataVolume != ""
}

func DigestOfClaim(spec v1alpha1.ImageSpec, claim *corev1.PersistentVolumeClaim) string {
	if claim == nil {
		return ""
	}
	if registry := spec.Source.Registry; registry != nil {
		if _, digest, pinned := strings.Cut(registry.Image, "@"); pinned {
			return digest
		}
	}
	if http := spec.Source.HTTP; http != nil && strings.HasPrefix(http.Checksum, "sha256:") {
		return http.Checksum
	}
	return "uid:" + string(claim.UID)
}

func SnapshotError(snapshot *unstructured.Unstructured) string {
	message, _, _ := unstructured.NestedString(snapshot.Object, "status", "error", "message")
	return message
}

func SnapshotReady(snapshot *unstructured.Unstructured) bool {
	ready, _, _ := unstructured.NestedBool(snapshot.Object, "status", "readyToUse")
	return ready
}

func RestoreBytes(snapshot *unstructured.Unstructured) int64 {
	text, _, _ := unstructured.NestedString(snapshot.Object, "status", "restoreSize")
	quantity, err := resource.ParseQuantity(text)
	if err != nil {
		return 0
	}
	return quantity.Value()
}

func MergeVersions(prev []v1alpha1.ImageVersion, latest *v1alpha1.ImageVersion, alive map[string]bool, keep int32) []v1alpha1.ImageVersion {
	ordered := make([]v1alpha1.ImageVersion, 0, len(prev)+1)
	if latest != nil {
		ordered = append(ordered, firstWithDigest(prev, *latest))
	}
	for _, version := range prev {
		if !slices.ContainsFunc(ordered, func(have v1alpha1.ImageVersion) bool { return have.Digest == version.Digest }) {
			ordered = append(ordered, version)
		}
	}
	merged := make([]v1alpha1.ImageVersion, 0, len(ordered))
	for _, version := range ordered {
		if alive[version.DataSource] {
			merged = append(merged, *version.DeepCopy())
		}
	}
	if keep > 0 && len(merged) > int(keep) {
		merged = merged[:keep]
	}
	return merged
}

func firstWithDigest(versions []v1alpha1.ImageVersion, latest v1alpha1.ImageVersion) v1alpha1.ImageVersion {
	index := slices.IndexFunc(versions, func(have v1alpha1.ImageVersion) bool { return have.Digest == latest.Digest })
	if index < 0 {
		return latest
	}
	return versions[index]
}

func BuildStatus(prev v1alpha1.ImageStatus, generation int64, r Result, now metav1.Time) v1alpha1.ImageStatus {
	status := *prev.DeepCopy()
	status.ObservedGeneration = generation
	status.Phase = r.Phase
	status.Progress = r.Progress
	status.Digest = r.Digest
	status.SizeBytes = r.SizeBytes
	status.DataSource = r.DataSource
	status.UploadURL = r.UploadURL
	status.Versions = slices.Clone(r.Versions)
	v1alpha1.SetCondition(&status.Conditions, readyCondition(generation, r, now))
	if r.AdditionalCAConflict == "" {
		meta.RemoveStatusCondition(&status.Conditions, ConditionAdditionalCA)
		return status
	}
	v1alpha1.SetCondition(&status.Conditions, metav1.Condition{
		Type:               ConditionAdditionalCA,
		Status:             metav1.ConditionFalse,
		Reason:             ReasonForeignConfigMap,
		Message:            r.AdditionalCAConflict,
		ObservedGeneration: generation,
		LastTransitionTime: now,
	})
	return status
}

func readyCondition(generation int64, r Result, now metav1.Time) metav1.Condition {
	condition := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		ObservedGeneration: generation,
		LastTransitionTime: now,
		Message:            r.Message,
	}
	if r.Phase == v1alpha1.ImagePhaseReady {
		condition.Status, condition.Reason = metav1.ConditionTrue, ReasonImported
		return condition
	}
	condition.Status, condition.Reason = metav1.ConditionFalse, r.Reason
	if condition.Reason == "" {
		condition.Reason = r.Phase
	}
	return condition
}

func percentOf(obj *unstructured.Unstructured) string {
	progress, _, _ := unstructured.NestedString(obj.Object, "status", "progress")
	if strings.HasSuffix(progress, "%") {
		return progress
	}
	return ""
}

func conditionStatus(obj *unstructured.Unstructured, conditionType string) string {
	condition, _ := findCondition(obj, conditionType)
	status, _ := condition["status"].(string)
	return status
}

func conditionMessage(obj *unstructured.Unstructured, conditionType string) string {
	condition, _ := findCondition(obj, conditionType)
	message, _ := condition["message"].(string)
	return message
}

func firstFalseMessage(obj *unstructured.Unstructured, conditionTypes ...string) string {
	for _, conditionType := range conditionTypes {
		condition, found := findCondition(obj, conditionType)
		if status, _ := condition["status"].(string); found && status == "False" {
			message, _ := condition["message"].(string)
			return message
		}
	}
	return ""
}

func findCondition(obj *unstructured.Unstructured, conditionType string) (map[string]any, bool) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, item := range conditions {
		condition, ok := item.(map[string]any)
		if ok && condition["type"] == conditionType {
			return condition, true
		}
	}
	return nil, false
}
