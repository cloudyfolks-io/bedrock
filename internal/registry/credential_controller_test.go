package registry

import (
	"context"
	"encoding/base64"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
	"github.com/cloudyfolks-io/bedrock/internal/registry/harbor"
)

func readyRegistry(name, project string) *v1alpha1.Registry {
	reg := registryFor(name, project)
	reg.Status.ProjectName = project
	reg.Status.Host = "registry.example.com"
	reg.Status.Conditions = []metav1.Condition{{Type: "Ready", Status: metav1.ConditionTrue, Reason: "Ready", LastTransitionTime: metav1.Now()}}
	return reg
}

func credentialFor(name, permission string) *v1alpha1.RegistryCredential {
	return &v1alpha1.RegistryCredential{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNamespace},
		Spec:       v1alpha1.RegistryCredentialSpec{RegistryRef: v1alpha1.RegistryReference{Name: "images"}, Permission: permission},
	}
}

func credentialWorld(t *testing.T, state *harborState, objects ...client.Object) (*CredentialReconciler, client.Client) {
	t.Helper()
	state.addProject("acme-images", 5)
	all := append([]client.Object{
		addonObject("registry", true), adminSecretObject(), namespaceObject(tenantNamespace), readyRegistry("images", "acme-images"),
	}, objects...)
	c := fakeCluster(t, all...)
	return &CredentialReconciler{Client: c, BaseURL: state.server.URL, Random: seeded(11)}, c
}

func reconcileCredential(t *testing.T, r *CredentialReconciler, name string) ctrl.Result {
	t.Helper()
	got, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: tenantNamespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func fetchCredential(t *testing.T, c client.Client, name string) *v1alpha1.RegistryCredential {
	t.Helper()
	var credential v1alpha1.RegistryCredential
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: name}, &credential); err != nil {
		t.Fatal(err)
	}
	return &credential
}

func credentialReady(credential *v1alpha1.RegistryCredential) metav1.Condition {
	for _, condition := range credential.Status.Conditions {
		if condition.Type == "Ready" {
			return condition
		}
	}
	return metav1.Condition{}
}

func dockerSecret(t *testing.T, c client.Client, name string) corev1.Secret {
	t.Helper()
	var secret corev1.Secret
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: name + "-docker"}, &secret); err != nil {
		t.Fatalf("secret %s-docker: %v", name, err)
	}
	return secret
}

func TestCredentialMakesTheRobotForEveryPermission(t *testing.T) {
	for _, permission := range []string{"pull", "pushPull", "manage"} {
		state := newHarborState(t)
		r, c := credentialWorld(t, state, credentialFor("ci", permission))
		reconcileCredential(t, r, "ci")
		credential := fetchCredential(t, c, "ci")
		if got := credentialReady(credential); got.Status != metav1.ConditionTrue {
			t.Fatalf("%s: condition %+v", permission, got)
		}
		robots := state.project("acme-images").robots
		if len(robots) != 1 || robots[0].name != "ci" {
			t.Fatalf("%s: robots %+v", permission, robots)
		}
		want, _ := harbor.AccessFor(permission)
		if len(robots[0].access) != len(want) {
			t.Errorf("%s: access %v", permission, robots[0].access)
		}
		if credential.Status.SecretName != "ci-docker" || credential.Status.Username != "robot$acme-images+ci" || credential.Status.RobotID != robots[0].id {
			t.Errorf("%s: status %+v", permission, credential.Status)
		}
	}
}

func TestCredentialSecretShapeGolden(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pushPull"))
	reconcileCredential(t, r, "ci")
	secret := dockerSecret(t, c, "ci")
	auth := base64.StdEncoding.EncodeToString([]byte("robot$acme-images+ci:secret-1"))
	want := `{"auths":{"registry.example.com":{"auth":"` + auth + `","password":"secret-1","username":"robot$acme-images+ci"}}}`
	if secret.Type != corev1.SecretTypeDockerConfigJson || string(secret.Data[".dockerconfigjson"]) != want {
		t.Errorf("type %s config %s", secret.Type, secret.Data[".dockerconfigjson"])
	}
	for key, value := range map[string]string{"host": "registry.example.com", "username": "robot$acme-images+ci", "password": "secret-1"} {
		if string(secret.Data[key]) != value {
			t.Errorf("%s = %q", key, secret.Data[key])
		}
	}
	if len(secret.Data) != 4 {
		t.Errorf("keys %d", len(secret.Data))
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != "ci" || secret.OwnerReferences[0].Kind != "RegistryCredential" {
		t.Errorf("owner %v", secret.OwnerReferences)
	}
	if secret.Labels["bedrock.cloudyfolks.io/kind"] != "RegistryCredential" || secret.Labels["bedrock.cloudyfolks.io/name"] != "ci" {
		t.Errorf("labels %v", secret.Labels)
	}
}

func TestCredentialSecondRunKeepsTheLogin(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	first := string(dockerSecret(t, c, "ci").Data["password"])
	reconcileCredential(t, r, "ci")
	if got := string(dockerSecret(t, c, "ci").Data["password"]); got != first || len(state.robotSets) != 0 || len(state.project("acme-images").robots) != 1 {
		t.Errorf("password %q sets %v robots %d", got, state.robotSets, len(state.project("acme-images").robots))
	}
}

func TestCredentialHealsAMissingSecret(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	first := string(dockerSecret(t, c, "ci").Data["password"])
	if err := c.Delete(context.Background(), &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: tenantNamespace, Name: "ci-docker"}}); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	healed := dockerSecret(t, c, "ci")
	if string(healed.Data["password"]) == first || len(state.robotSets) != 1 {
		t.Fatalf("password %q sets %v", healed.Data["password"], state.robotSets)
	}
	if got := state.project("acme-images").robots[0].secret; got != string(healed.Data["password"]) {
		t.Errorf("the robot secret %q is not the Secret %q", got, healed.Data["password"])
	}
}

func TestCredentialHealsABrokenSecret(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	secret := dockerSecret(t, c, "ci")
	secret.Data = map[string][]byte{"password": []byte("x")}
	if err := c.Update(context.Background(), &secret); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	if len(state.robotSets) != 1 || len(dockerSecret(t, c, "ci").Data) != 4 {
		t.Errorf("sets %v keys %d", state.robotSets, len(dockerSecret(t, c, "ci").Data))
	}
}

func TestCredentialResetsTheSecretWhenTheAnnotationChanges(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	first := string(dockerSecret(t, c, "ci").Data["password"])
	credential := fetchCredential(t, c, "ci")
	credential.Annotations = map[string]string{AnnotationResetSecret: "one"}
	if err := c.Update(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	second := string(dockerSecret(t, c, "ci").Data["password"])
	if second == first || len(state.robotSets) != 1 || fetchCredential(t, c, "ci").Status.SecretReset != "one" {
		t.Fatalf("password %q sets %v", second, state.robotSets)
	}
	reconcileCredential(t, r, "ci")
	if got := string(dockerSecret(t, c, "ci").Data["password"]); got != second || len(state.robotSets) != 1 {
		t.Errorf("a second run changed the login: %q %v", got, state.robotSets)
	}
	credential = fetchCredential(t, c, "ci")
	credential.Annotations[AnnotationResetSecret] = "two"
	if err := c.Update(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	if len(state.robotSets) != 2 {
		t.Errorf("sets %v", state.robotSets)
	}
}

func TestCredentialWithTheResetAnnotationAtCreateDoesNotResetTwice(t *testing.T) {
	state := newHarborState(t)
	credential := credentialFor("ci", "pull")
	credential.Annotations = map[string]string{AnnotationResetSecret: "start"}
	r, c := credentialWorld(t, state, credential)
	reconcileCredential(t, r, "ci")
	reconcileCredential(t, r, "ci")
	if len(state.robotSets) != 0 || fetchCredential(t, c, "ci").Status.SecretReset != "start" {
		t.Errorf("sets %v", state.robotSets)
	}
}

func TestCredentialDeleteRemovesTheRobotAndTheFinalizer(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	if err := c.Delete(context.Background(), fetchCredential(t, c, "ci")); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	var gone v1alpha1.RegistryCredential
	err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: "ci"}, &gone)
	if !apierrors.IsNotFound(err) || len(state.project("acme-images").robots) != 0 {
		t.Fatalf("object error %v robots %d", err, len(state.project("acme-images").robots))
	}
}

func TestCredentialDeleteAfterTheProjectIsGone(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	state.mu.Lock()
	delete(state.projects, "acme-images")
	state.mu.Unlock()
	if err := c.Delete(context.Background(), fetchCredential(t, c, "ci")); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	var gone v1alpha1.RegistryCredential
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: "ci"}, &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the object stayed: %v", err)
	}
}

func TestCredentialThatNeverOwnedARobotLeavesItAlone(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "ci", secret: "theirs"}}
	reconcileCredential(t, r, "ci")
	credential := fetchCredential(t, c, "ci")
	if got := credentialReady(credential); got.Reason != "NameTaken" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if err := c.Delete(context.Background(), credential); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	if len(state.project("acme-images").robots) != 1 || state.project("acme-images").robots[0].secret != "theirs" {
		t.Error("a foreign robot was changed or deleted")
	}
}

func TestCredentialAdoptsARobotWithTheAnnotation(t *testing.T) {
	state := newHarborState(t)
	credential := credentialFor("ci", "pull")
	credential.Annotations = map[string]string{AnnotationAdopt: "true"}
	r, c := credentialWorld(t, state, credential)
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "ci", secret: "old"}}
	reconcileCredential(t, r, "ci")
	got := fetchCredential(t, c, "ci")
	secret := dockerSecret(t, c, "ci")
	if credentialReady(got).Status != metav1.ConditionTrue || got.Status.RobotID != 700 || string(secret.Data["password"]) == "old" || len(state.robotSets) != 1 {
		t.Fatalf("status %+v sets %v", got.Status, state.robotSets)
	}
	if state.project("acme-images").robots[0].secret != string(secret.Data["password"]) {
		t.Error("the robot secret is not the Secret")
	}
}

func TestCredentialResumesACreateThatLostItsStatus(t *testing.T) {
	state := newHarborState(t)
	credential := credentialFor("ci", "pull")
	credential.Status.RobotName = "ci"
	r, c := credentialWorld(t, state, credential)
	state.project("acme-images").robots = []fakeRobot{{id: 700, name: "ci", secret: "lost"}}
	reconcileCredential(t, r, "ci")
	got := fetchCredential(t, c, "ci")
	if credentialReady(got).Status != metav1.ConditionTrue || got.Status.RobotID != 700 || len(state.project("acme-images").robots) != 1 {
		t.Fatalf("status %+v", got.Status)
	}
	if string(dockerSecret(t, c, "ci").Data["password"]) == "lost" {
		t.Error("the lost secret was reused")
	}
}

func TestCredentialWaitsForTheRegistry(t *testing.T) {
	state := newHarborState(t)
	c := fakeCluster(t, addonObject("registry", true), adminSecretObject(), namespaceObject(tenantNamespace), credentialFor("ci", "pull"))
	r := &CredentialReconciler{Client: c, BaseURL: state.server.URL}
	reconcileCredential(t, r, "ci")
	if got := credentialReady(fetchCredential(t, c, "ci")); got.Reason != "RegistryNotFound" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	unready := registryFor("images", "acme-images")
	if err := c.Create(context.Background(), unready); err != nil {
		t.Fatal(err)
	}
	reconcileCredential(t, r, "ci")
	if got := credentialReady(fetchCredential(t, c, "ci")); got.Reason != "RegistryNotReady" {
		t.Fatalf("condition %+v", got)
	}
}

func TestCredentialIsNotAvailableWithTheAddonOff(t *testing.T) {
	state := newHarborState(t)
	c := fakeCluster(t, addonObject("registry", false), adminSecretObject(), namespaceObject(tenantNamespace), readyRegistry("images", "acme-images"), credentialFor("ci", "pull"))
	r := &CredentialReconciler{Client: c, BaseURL: state.server.URL}
	reconcileCredential(t, r, "ci")
	if got := credentialReady(fetchCredential(t, c, "ci")); got.Reason != "RegistryNotAvailable" {
		t.Fatalf("condition %+v", got)
	}
}

func TestCredentialAddsItsFinalizer(t *testing.T) {
	state := newHarborState(t)
	r, c := credentialWorld(t, state, credentialFor("ci", "pull"))
	reconcileCredential(t, r, "ci")
	if !controllerutil.ContainsFinalizer(fetchCredential(t, c, "ci"), platform.Finalizer("RegistryCredential")) {
		t.Error("no finalizer")
	}
}

func TestCredentialOfARegistryEnqueuesItsCredentials(t *testing.T) {
	other := credentialFor("other", "pull")
	other.Spec.RegistryRef.Name = "elsewhere"
	c := fakeCluster(t, credentialFor("ci", "pull"), other)
	requests := credentialsOfRegistry(c)(context.Background(), readyRegistry("images", "acme-images"))
	names := []string{}
	for _, request := range requests {
		names = append(names, request.Name)
	}
	if !slices.Equal(names, []string{"ci"}) {
		t.Errorf("requests %v", names)
	}
}
