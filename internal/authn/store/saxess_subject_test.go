package store

import (
	"context"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestSAxessApprovedSubjectCannotBeReplaced(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	user := &v1alpha1.User{
		ObjectMeta: metav1.ObjectMeta{Name: "admin", Namespace: release.SystemNamespace, UID: "approved-uid"},
		Spec:       v1alpha1.UserSpec{Groups: []string{v1alpha1.GroupAdmins}},
	}
	request := v1alpha1.AuthRequest{Status: v1alpha1.AuthRequestStatus{
		Subject: "admin", Login: v1alpha1.LoginState{Primary: v1alpha1.MethodSAxess, Provider: "approved-uid"},
	}}
	approved := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	if err := CheckAuthRequestSubject(ctx, approved, request, true); err != nil {
		t.Fatal(err)
	}
	if err := CheckAuthRequestSubject(ctx, approved, request, false); err == nil {
		t.Fatal("disabled feature accepted a completed sAxess request")
	}
	user.UID = "replacement-uid"
	replacement := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	if err := CheckAuthRequestSubject(ctx, replacement, request, true); err == nil {
		t.Fatal("replacement user inherited sAxess approval")
	}
	user.UID = "approved-uid"
	user.Spec.Groups = nil
	demoted := fake.NewClientBuilder().WithScheme(scheme).WithObjects(user).Build()
	if err := CheckAuthRequestSubject(ctx, demoted, request, true); err == nil {
		t.Fatal("demoted user inherited sAxess approval")
	}
}
