package v1alpha1

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

const AnnotationRestoreHold = "bedrock.cloudyfolks.io/restore-hold"

func RestoreHolder(obj metav1.Object) (string, bool) {
	holder, held := obj.GetAnnotations()[AnnotationRestoreHold]
	return holder, held
}
