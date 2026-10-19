package registry

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const tenantNamespace = "t-acme"

func registryFor(name, projectName string) *v1alpha1.Registry {
	return &v1alpha1.Registry{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: tenantNamespace},
		Spec:       v1alpha1.RegistryProjectSpec{Name: projectName},
	}
}

func namespaceObject(name string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func registryWorld(t *testing.T, state *harborState, objects ...client.Object) (*RegistryReconciler, client.Client) {
	t.Helper()
	all := append([]client.Object{
		addonObject("registry", true), adminSecretObject(), namespaceObject(tenantNamespace),
		setting("registry.host", "registry.example.com"),
	}, objects...)
	c := fakeCluster(t, all...)
	return &RegistryReconciler{Client: c, BaseURL: state.server.URL}, c
}

func reconcileRegistry(t *testing.T, r *RegistryReconciler, name string) ctrl.Result {
	t.Helper()
	got, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: tenantNamespace, Name: name}})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func fetchRegistry(t *testing.T, c client.Client, name string) *v1alpha1.Registry {
	t.Helper()
	var reg v1alpha1.Registry
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: name}, &reg); err != nil {
		t.Fatal(err)
	}
	return &reg
}

func readyCondition(reg *v1alpha1.Registry) metav1.Condition {
	condition := meta.FindStatusCondition(reg.Status.Conditions, "Ready")
	if condition == nil {
		return metav1.Condition{}
	}
	return *condition
}

func TestRegistryMakesTheProject(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	got := reconcileRegistry(t, r, "images")
	if got.RequeueAfter.Minutes() != 5 {
		t.Errorf("requeue %s", got.RequeueAfter)
	}
	reg := fetchRegistry(t, c, "images")
	condition := readyCondition(reg)
	if condition.Status != metav1.ConditionTrue || condition.Reason != "Ready" {
		t.Fatalf("condition %+v", condition)
	}
	project := state.project("acme-images")
	if project == nil || reg.Status.ProjectID != project.id || reg.Status.ProjectName != "acme-images" || reg.Status.Host != "registry.example.com" {
		t.Errorf("status %+v project %+v", reg.Status, project)
	}
	if project.quota != 5*1024*1024*1024 || reg.Status.HardBytes != project.quota || reg.Status.UsedBytes != 42 {
		t.Errorf("quota %d status %+v", project.quota, reg.Status)
	}
	if !controllerutil.ContainsFinalizer(reg, platform.Finalizer("Registry")) {
		t.Error("no finalizer")
	}
}

func TestRegistryNameDefaultsToTheObjectName(t *testing.T) {
	state := newHarborState(t)
	r, _ := registryWorld(t, state, registryFor("acme-default", ""))
	reconcileRegistry(t, r, "acme-default")
	if state.project("acme-default") == nil {
		t.Fatal("no project with the object name")
	}
}

func TestRegistryQuotaFromTheSpecAndTheDefaultSetting(t *testing.T) {
	state := newHarborState(t)
	zero := registryFor("unlimited", "acme-unlimited")
	zero.Spec.QuotaGB = ptr.To(int64(0))
	big := registryFor("big", "acme-big")
	big.Spec.QuotaGB = ptr.To(int64(20))
	r, _ := registryWorld(t, state, zero, big, registryFor("def", "acme-def"), setting("registry.default-quota-gb", "7"))
	for _, name := range []string{"unlimited", "big", "def"} {
		reconcileRegistry(t, r, name)
	}
	if got := state.project("acme-unlimited").quota; got != -1 {
		t.Errorf("unlimited quota %d", got)
	}
	if got := state.project("acme-big").quota; got != 20*1024*1024*1024 {
		t.Errorf("big quota %d", got)
	}
	if got := state.project("acme-def").quota; got != 7*1024*1024*1024 {
		t.Errorf("default quota %d", got)
	}
}

func TestRegistryChangesTheQuota(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.QuotaGB = ptr.To(int64(10))
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	latest := fetchRegistry(t, c, "images")
	latest.Spec.QuotaGB = ptr.To(int64(3))
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	reconcileRegistry(t, r, "images")
	if got := state.project("acme-images").quota; got != 3*1024*1024*1024 {
		t.Errorf("quota %d", got)
	}
	if got := fetchRegistry(t, c, "images").Status.HardBytes; got != 3*1024*1024*1024 {
		t.Errorf("status hard bytes %d", got)
	}
}

func TestRegistryReportsNameTakenForAForeignProject(t *testing.T) {
	state := newHarborState(t)
	state.addProject("acme-images", 1)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	reg := fetchRegistry(t, c, "images")
	condition := readyCondition(reg)
	if condition.Status != metav1.ConditionFalse || condition.Reason != "NameTaken" || reg.Status.ProjectName != "" || reg.Status.ProjectID != 0 {
		t.Fatalf("condition %+v status %+v", condition, reg.Status)
	}
	if len(state.creates) != 0 {
		t.Errorf("creates %v", state.creates)
	}
}

func TestRegistryAdoptsAForeignProjectWithTheAnnotation(t *testing.T) {
	state := newHarborState(t)
	existing := state.addProject("acme-images", 1)
	reg := registryFor("images", "acme-images")
	reg.Annotations = map[string]string{AnnotationAdopt: "true"}
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	got := fetchRegistry(t, c, "images")
	if readyCondition(got).Status != metav1.ConditionTrue || got.Status.ProjectID != existing.id || got.Status.ProjectName != "acme-images" {
		t.Fatalf("status %+v", got.Status)
	}
	if len(state.creates) != 0 {
		t.Errorf("creates %v", state.creates)
	}
}

func TestRegistryResumesACreateThatLostItsStatus(t *testing.T) {
	state := newHarborState(t)
	existing := state.addProject("acme-images", 5)
	reg := registryFor("images", "acme-images")
	reg.Status.ProjectName = "acme-images"
	reg.Status.ProjectMarkedAt = ptr.To(metav1.NewTime(time.Now().Add(-time.Minute)))
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	got := fetchRegistry(t, c, "images")
	if readyCondition(got).Reason != "Ready" || got.Status.ProjectID != existing.id {
		t.Fatalf("status %+v", got.Status)
	}
	if len(state.creates) != 0 || len(state.projects) != 1 {
		t.Errorf("creates %v projects %d", state.creates, len(state.projects))
	}
}

func TestRegistryMakesTheProjectAgainWhenItWasDeletedInHarbor(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	state.mu.Lock()
	delete(state.projects, "acme-images")
	state.mu.Unlock()
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") == nil || readyCondition(fetchRegistry(t, c, "images")).Reason != "Ready" {
		t.Fatal("the project was not made again")
	}
}

func TestRegistryRunsTwiceWithoutASecondProject(t *testing.T) {
	state := newHarborState(t)
	r, _ := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	reconcileRegistry(t, r, "images")
	if len(state.creates) != 1 {
		t.Errorf("creates %v", state.creates)
	}
}

func TestRegistryRefusesBadAndReservedNames(t *testing.T) {
	cases := []struct {
		object string
		name   string
		reason string
	}{
		{"short", "ab", "NameInvalid"},
		{"upper", "Acme-Images", "NameInvalid"},
		{"dash", "acme-", "NameInvalid"},
		{"library", "library", "NameReserved"},
		{"mirror", "mirror-docker", "NameReserved"},
	}
	for _, tc := range cases {
		state := newHarborState(t)
		r, c := registryWorld(t, state, registryFor(tc.object, tc.name))
		reconcileRegistry(t, r, tc.object)
		if got := readyCondition(fetchRegistry(t, c, tc.object)); got.Reason != tc.reason || got.Status != metav1.ConditionFalse {
			t.Errorf("%s: condition %+v", tc.name, got)
		}
		if len(state.creates) != 0 {
			t.Errorf("%s: creates %v", tc.name, state.creates)
		}
	}
}

func TestRegistryUsesAReservedNameOnlyWithTheApproval(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("platform", "platform")
	reg.Annotations = map[string]string{AnnotationReservedNameApproved: "true"}
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "platform")
	if readyCondition(fetchRegistry(t, c, "platform")).Reason != "Ready" || state.project("platform") == nil {
		t.Fatal("an approved reserved name was refused")
	}
}

func TestRegistryIsNotAvailableWithTheAddonOff(t *testing.T) {
	state := newHarborState(t)
	c := fakeCluster(t, addonObject("registry", false), adminSecretObject(), namespaceObject(tenantNamespace), registryFor("images", "acme-images"))
	r := &RegistryReconciler{Client: c, BaseURL: state.server.URL}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: tenantNamespace, Name: "images"}}); err != nil {
		t.Fatal(err)
	}
	reg := fetchRegistry(t, c, "images")
	if got := readyCondition(reg); got.Reason != "RegistryNotAvailable" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if len(state.projects) != 0 {
		t.Error("a project was made with the addon off")
	}
}

func TestRegistryIsNotAvailableWithoutTheAddonObject(t *testing.T) {
	state := newHarborState(t)
	c := fakeCluster(t, namespaceObject(tenantNamespace), registryFor("images", "acme-images"))
	r := &RegistryReconciler{Client: c, BaseURL: state.server.URL}
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: tenantNamespace, Name: "images"}}); err != nil {
		t.Fatal(err)
	}
	if got := readyCondition(fetchRegistry(t, c, "images")); got.Reason != "RegistryNotAvailable" {
		t.Fatalf("condition %+v", got)
	}
}

func TestRegistryIsNotAvailableWhenHarborIsDown(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	state.server.Close()
	reconcileRegistry(t, r, "images")
	if got := readyCondition(fetchRegistry(t, c, "images")); got.Reason != "RegistryNotAvailable" {
		t.Fatalf("condition %+v", got)
	}
}

func retentionOf(state *harborState, project string) map[string]any {
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.retentions[state.projects[project].retentionID]
}

func TestRegistrySetsAndClearsTheRetentionPolicy(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.Retention = &v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(5)), RetainDays: ptr.To(int32(30)), Schedule: "daily"}
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	policy := retentionOf(state, "acme-images")
	rules, _ := policy["rules"].([]any)
	if len(rules) != 2 {
		t.Fatalf("policy %v", policy)
	}
	trigger := policy["trigger"].(map[string]any)["settings"].(map[string]any)["cron"]
	if trigger != "0 0 0 * * *" {
		t.Errorf("cron %v", trigger)
	}
	latest := fetchRegistry(t, c, "images")
	latest.Spec.Retention = nil
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	reconcileRegistry(t, r, "images")
	cleared := retentionOf(state, "acme-images")
	if rules, _ := cleared["rules"].([]any); len(rules) != 0 {
		t.Errorf("rules after clearing %v", cleared["rules"])
	}
}

func TestRegistryWithoutRetentionMakesNoPolicy(t *testing.T) {
	state := newHarborState(t)
	r, _ := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	if len(state.retentions) != 0 {
		t.Errorf("retentions %v", state.retentions)
	}
}

func hookSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Namespace: tenantNamespace, Name: "hook"},
		Data:       map[string][]byte{"token": []byte("Bearer hook-token-value")},
	}
}

func TestRegistrySetsAndRemovesTheWebhook(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.Webhook = &v1alpha1.RegistryWebhook{URL: "https://hooks.example.test/x", AuthHeaderSecretRef: &v1alpha1.RegistrySecretKeyRef{Name: "hook", Key: "token"}}
	r, c := registryWorld(t, state, reg, hookSecret())
	reconcileRegistry(t, r, "images")
	hooks := state.project("acme-images").webhooks
	if len(hooks) != 1 {
		t.Fatalf("webhooks %v", hooks)
	}
	target := hooks[0].body["targets"].([]any)[0].(map[string]any)
	if target["address"] != "https://hooks.example.test/x" || target["auth_header"] != "Bearer hook-token-value" {
		t.Errorf("target %v", target)
	}
	reconcileRegistry(t, r, "images")
	if len(state.project("acme-images").webhooks) != 1 {
		t.Error("a second run made a second policy")
	}
	latest := fetchRegistry(t, c, "images")
	latest.Spec.Webhook = nil
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	reconcileRegistry(t, r, "images")
	if len(state.project("acme-images").webhooks) != 0 {
		t.Error("the webhook stayed")
	}
}

func TestRegistryReportsAMissingWebhookSecretWithoutLeakingValues(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.Webhook = &v1alpha1.RegistryWebhook{URL: "https://hooks.example.test/x", AuthHeaderSecretRef: &v1alpha1.RegistrySecretKeyRef{Name: "hook", Key: "token"}}
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	got := readyCondition(fetchRegistry(t, c, "images"))
	if got.Reason != "WebhookSecretMissing" || got.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", got)
	}
	if len(state.project("acme-images").webhooks) != 0 {
		t.Error("a webhook was made without its secret")
	}
}

func deleteRegistry(t *testing.T, c client.Client, name string) {
	t.Helper()
	if err := c.Delete(context.Background(), fetchRegistry(t, c, name)); err != nil {
		t.Fatal(err)
	}
}

func registryGone(c client.Client, name string) bool {
	var reg v1alpha1.Registry
	err := c.Get(context.Background(), client.ObjectKey{Namespace: tenantNamespace, Name: name}, &reg)
	return apierrors.IsNotFound(err)
}

func TestRegistryDeletesAnEmptyProject(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") != nil || !registryGone(c, "images") {
		t.Fatal("the project or the object stayed")
	}
}

func TestRegistryWithRepositoriesStaysTerminatingWithoutForce(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 3)
	deleteRegistry(t, c, "images")
	got := reconcileRegistry(t, r, "images")
	reg := fetchRegistry(t, c, "images")
	condition := readyCondition(reg)
	if condition.Reason != "NotEmpty" || !strings.Contains(condition.Message, "3 repositories") || reg.Status.RepositoryCount != 3 {
		t.Fatalf("condition %+v status %+v", condition, reg.Status)
	}
	if got.RequeueAfter == 0 || state.project("acme-images") == nil || len(state.project("acme-images").repos) != 3 {
		t.Error("data was touched or no requeue")
	}
}

func TestRegistryForceDestroyDeletesRobotsRepositoriesAndProject(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.ForceDestroy = true
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 250)
	state.project("acme-images").robots = []fakeRobot{{id: 900, name: "ci"}}
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") != nil || !registryGone(c, "images") {
		t.Fatalf("the project or the object stayed: repos left %v", state.projects)
	}
}

func TestRegistryDeleteWithAFinishedPurgeRoundStopsAtThePageLimit(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.ForceDestroy = true
	r, c := registryWorld(t, state, reg)
	r.PurgePages = 2
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 350)
	deleteRegistry(t, c, "images")
	got := reconcileRegistry(t, r, "images")
	if condition := readyCondition(fetchRegistry(t, c, "images")); condition.Reason != "PurgeIncomplete" || condition.Status != metav1.ConditionFalse {
		t.Fatalf("condition %+v", condition)
	}
	if left := len(state.project("acme-images").repos); left != 150 || got.RequeueAfter == 0 {
		t.Errorf("repositories left %d, requeue %s", left, got.RequeueAfter)
	}
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") != nil || !registryGone(c, "images") {
		t.Error("the next round did not finish the purge")
	}
}

func TestRegistryOfADeletingNamespaceActsAsForce(t *testing.T) {
	state := newHarborState(t)
	leaving := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: tenantNamespace, DeletionTimestamp: ptr.To(metav1.Now()), Finalizers: []string{"kubernetes"}}}
	c := fakeCluster(t, addonObject("registry", true), adminSecretObject(), leaving, setting("registry.host", "registry.example.com"))
	r := &RegistryReconciler{Client: c, BaseURL: state.server.URL}
	reg := registryFor("images", "acme-images")
	if err := c.Create(context.Background(), reg); err != nil {
		t.Fatal(err)
	}
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 5)
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") != nil || !registryGone(c, "images") {
		t.Fatal("a registry of a deleting namespace kept its project")
	}
}

func TestRegistryThatNeverOwnedItsProjectLeavesTheForeignProjectAlone(t *testing.T) {
	state := newHarborState(t)
	state.addProject("acme-images", 1)
	state.addRepos("acme-images", 2)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if !registryGone(c, "images") {
		t.Fatal("the object stayed")
	}
	if project := state.project("acme-images"); project == nil || len(project.repos) != 2 {
		t.Fatal("a foreign project was changed")
	}
}

func TestRegistryDeleteWaitsWhenTheAddonIsOff(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	addon := &v1alpha1.Addon{}
	if err := c.Get(context.Background(), client.ObjectKey{Name: "registry"}, addon); err != nil {
		t.Fatal(err)
	}
	addon.Spec.Enabled = false
	if err := c.Update(context.Background(), addon); err != nil {
		t.Fatal(err)
	}
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if registryGone(c, "images") || state.project("acme-images") == nil {
		t.Fatal("the delete went on with the addon off")
	}
	if got := readyCondition(fetchRegistry(t, c, "images")); got.Reason != "RegistryNotAvailable" {
		t.Errorf("condition %+v", got)
	}
}

func TestRegistrySettingsErrorsAreReported(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"), setting("registry.default-quota-gb", "-5"))
	reconcileRegistry(t, r, "images")
	if got := readyCondition(fetchRegistry(t, c, "images")); got.Reason != "InvalidSettings" {
		t.Errorf("condition %+v", got)
	}
}

func TestRepositoryPathStripsTheProject(t *testing.T) {
	cases := map[[2]string]string{
		{"acme", "acme/app"}:      "app",
		{"acme", "acme/team/app"}: "team/app",
		{"acme", "other/app"}:     "other/app",
	}
	for input, want := range cases {
		if got := repositoryPath(input[0], input[1]); got != want {
			t.Errorf("%v -> %q, want %q", input, got, want)
		}
	}
}

func TestRegistryUsageRefreshesEveryFiveMinutes(t *testing.T) {
	if usageInterval != 5*time.Minute {
		t.Fatalf("usage interval %s", usageInterval)
	}
	state := newHarborState(t)
	r, _ := registryWorld(t, state, registryFor("images", "acme-images"))
	if got := reconcileRegistry(t, r, "images"); got.RequeueAfter != 5*time.Minute {
		t.Errorf("requeue %s", got.RequeueAfter)
	}
}

func TestRegistryUsageFollowsHarbor(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 4)
	reconcileRegistry(t, r, "images")
	reg := fetchRegistry(t, c, "images")
	if reg.Status.RepositoryCount != 4 || reg.Status.UsedBytes != 42 {
		t.Errorf("status %+v", reg.Status)
	}
}

func TestRegistryKeepsTheOldUsageWhenTheSummaryFails(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	state.addRepos("acme-images", 2)
	reconcileRegistry(t, r, "images")
	before := fetchRegistry(t, c, "images").Status
	state.mu.Lock()
	state.failSummary = true
	state.mu.Unlock()
	state.addRepos("acme-images", 3)
	reconcileRegistry(t, r, "images")
	after := fetchRegistry(t, c, "images")
	if after.Status.UsedBytes != before.UsedBytes || after.Status.HardBytes != before.HardBytes || after.Status.RepositoryCount != 2 {
		t.Errorf("usage changed: before %+v after %+v", before, after.Status)
	}
	if readyCondition(after).Status != metav1.ConditionTrue {
		t.Errorf("condition %+v", readyCondition(after))
	}
}

func TestRegistryDoesNotClaimAProjectThatIsOlderThanItsMarker(t *testing.T) {
	state := newHarborState(t)
	foreign := state.addProject("acme-images", 1)
	foreign.created = time.Now().Add(-time.Hour)
	reg := registryFor("images", "acme-images")
	reg.Status.ProjectName = "acme-images"
	reg.Status.ProjectMarkedAt = ptr.To(metav1.Now())
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	got := fetchRegistry(t, c, "images")
	if readyCondition(got).Reason != "NameTaken" || got.Status.ProjectID != 0 {
		t.Fatalf("status %+v", got.Status)
	}
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if !registryGone(c, "images") || state.project("acme-images") == nil {
		t.Fatal("a foreign project was deleted or the object stayed")
	}
}

func TestRegistryDoesNotClaimAProjectWithAnotherID(t *testing.T) {
	state := newHarborState(t)
	state.addProject("acme-images", 1)
	state.addRepos("acme-images", 2)
	reg := registryFor("images", "acme-images")
	reg.Spec.ForceDestroy = true
	reg.Status.ProjectName = "acme-images"
	reg.Status.ProjectID = 5
	reg.Status.ProjectMarkedAt = ptr.To(metav1.NewTime(time.Now().Add(-time.Hour)))
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	if got := readyCondition(fetchRegistry(t, c, "images")); got.Reason != "NameTaken" {
		t.Fatalf("condition %+v", got)
	}
	if len(state.project("acme-images").repos) != 2 {
		t.Fatal("a foreign project was changed")
	}
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if !registryGone(c, "images") || state.project("acme-images") == nil || len(state.project("acme-images").repos) != 2 {
		t.Fatal("a foreign project was deleted")
	}
}

func TestRegistryDoesNotClaimAProjectThatAppearsAfterAFailedCreate(t *testing.T) {
	state := newHarborState(t)
	state.failProjectCreate = true
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	got := fetchRegistry(t, c, "images")
	if got.Status.ProjectName != "" || got.Status.ProjectMarkedAt != nil {
		t.Fatalf("the marker stayed after a failed create: %+v", got.Status)
	}
	state.addRepos("acme-images", 0)
	state.addProject("acme-images", 1)
	reconcileRegistry(t, r, "images")
	got = fetchRegistry(t, c, "images")
	if readyCondition(got).Reason != "NameTaken" || got.Status.ProjectID != 0 {
		t.Fatalf("status %+v", got.Status)
	}
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if state.project("acme-images") == nil {
		t.Fatal("the foreign project was deleted")
	}
}

func TestRegistryRecordsTheProjectIDAtOnce(t *testing.T) {
	state := newHarborState(t)
	r, c := registryWorld(t, state, registryFor("images", "acme-images"))
	reconcileRegistry(t, r, "images")
	got := fetchRegistry(t, c, "images")
	if got.Status.ProjectID != state.project("acme-images").id || got.Status.ProjectMarkedAt == nil {
		t.Fatalf("status %+v", got.Status)
	}
}

func TestRegistryForceDeleteRemovesMoreThanOnePageOfRobotsAndNoForeignRobot(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.ForceDestroy = true
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	own := state.project("acme-images")
	for i := range 230 {
		own.robots = append(own.robots, fakeRobot{id: int64(1000 + i), name: "r" + strconv.Itoa(i), created: time.Now()})
	}
	other := state.addProject("other-project", 1)
	other.robots = []fakeRobot{{id: 5000, name: "keep", created: time.Now()}}
	state.mu.Lock()
	state.ignoreRobotFilter = true
	state.mu.Unlock()
	deleteRegistry(t, c, "images")
	reconcileRegistry(t, r, "images")
	if !registryGone(c, "images") || state.project("acme-images") != nil {
		t.Fatal("the project stayed")
	}
	if kept := state.project("other-project"); kept == nil || len(kept.robots) != 1 {
		t.Fatal("a robot of another project was deleted")
	}
}
