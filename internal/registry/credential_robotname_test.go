package registry

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func namedRobotCredential(name, robotName string, annotations map[string]string) *v1alpha1.RegistryCredential {
	credential := credentialFor(name, "pull")
	credential.Spec.RobotName = robotName
	credential.Annotations = annotations
	return credential
}

func TestCredentialNamesTheRobotWithTheRobotName(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, namedRobotCredential("c1", "customer-ci", nil))
	reconcileCredential(t, r, "c1")
	got := fetchCredential(t, c, "c1")
	robots := state.project("acme-images").robots
	if len(robots) != 1 || robots[0].name != "customer-ci" {
		t.Fatalf("robots %+v", robots)
	}
	if got.Status.Username != "robot$acme-images+customer-ci" || got.Status.RobotName != "customer-ci" || credentialReady(got).Status != metav1.ConditionTrue {
		t.Fatalf("status %+v", got.Status)
	}
	if dockerSecret(t, c, "c1").Name != "c1-docker" {
		t.Fatal("the Secret name follows the object name")
	}
}

func TestCredentialRefusesAnExistingRobotNameWithoutAdoption(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, namedRobotCredential("c1", "customer-ci", nil))
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "customer-ci", secret: "theirs"}}
	reconcileCredential(t, r, "c1")
	if got := credentialReady(fetchCredential(t, c, "c1")); got.Reason != "NameTaken" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if robots := state.project("acme-images").robots; len(robots) != 1 || robots[0].secret != "theirs" || len(state.robotSets) != 0 {
		t.Fatal("the existing robot changed")
	}
}

func TestCredentialAdoptsAnExistingRobotNameWithTheAnnotation(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, namedRobotCredential("c1", "customer-ci", map[string]string{AnnotationAdopt: "true"}))
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "customer-ci", secret: "old"}}
	reconcileCredential(t, r, "c1")
	got := fetchCredential(t, c, "c1")
	if credentialReady(got).Status != metav1.ConditionTrue || got.Status.RobotID != 700 || got.Status.Username != "robot$acme-images+customer-ci" {
		t.Fatalf("status %+v", got.Status)
	}
	if len(state.project("acme-images").robots) != 1 {
		t.Fatal("a second robot was made")
	}
}

func TestCredentialAdoptionRefusesARobotAnotherCredentialHolds(t *testing.T) {
	state := newHarborState(t)
	holder := namedRobotCredential("first", "customer-ci", nil)
	holder.Status.RobotID, holder.Status.RobotName = 700, "customer-ci"
	second := namedRobotCredential("second", "customer-ci", map[string]string{AnnotationAdopt: "true"})
	r, c := credentialWorld(t, state, holder, second)
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "customer-ci", secret: "held"}}
	reconcileCredential(t, r, "second")
	if got := credentialReady(fetchCredential(t, c, "second")); got.Reason != "RobotClaimed" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if robots := state.project("acme-images").robots; robots[0].secret != "held" || len(state.robotSets) != 0 {
		t.Fatal("the held robot changed")
	}
}

func TestCredentialDeleteRemovesTheRobotOfTheRobotName(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, namedRobotCredential("c1", "customer-ci", nil))
	reconcileCredential(t, r, "c1")
	if err := c.Delete(context.Background(), fetchCredential(t, c, "c1")); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "c1")
	if len(state.project("acme-images").robots) != 0 {
		t.Fatal("the robot is still there")
	}
	var gone v1alpha1.RegistryCredential
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: "c1"}, &gone); err == nil {
		t.Fatal("the credential is still there")
	}
}
