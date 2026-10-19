package container

import (
	"context"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func newBuildReconciler(c client.Client) *BuildReconciler {
	return &BuildReconciler{Client: c, Now: func() time.Time { return fixedNow }}
}

func fixtureBuild(namespace, name string) v1alpha1.Build {
	return v1alpha1.Build{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec: v1alpha1.BuildSpec{
			Type: v1alpha1.BuildTypeDockerfile,
			Source: v1alpha1.BuildSource{Git: v1alpha1.BuildGitSource{
				URL: "https://git.test/acme/shop.git", Revision: "0123456789abcdef", SecretRef: "git",
			}},
			ContextPath:    ".",
			DockerfilePath: "Dockerfile",
			Output:         v1alpha1.BuildOutput{Image: "registry.test/acme/shop:1", PushSecretRef: "push"},
			Size:           v1alpha1.BuildSizeMedium,
			Network:        v1alpha1.BuildNetwork{SubnetRef: "default"},
		},
	}
}

func createBuildSecrets(t testing.TB, c client.Client, namespace string) {
	t.Helper()
	createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "push"}, Type: corev1.SecretTypeDockerConfigJson, Data: map[string][]byte{corev1.DockerConfigJsonKey: []byte("{}")}})
	createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "git"}, Type: corev1.SecretTypeOpaque, Data: map[string][]byte{"username": []byte("robot"), "token": []byte("s3cr3t")}})
}

func setKataAddon(t testing.TB, c client.Client, ready metav1.ConditionStatus) {
	t.Helper()
	ctx := context.Background()
	addon := &v1alpha1.Addon{ObjectMeta: metav1.ObjectMeta{Name: "kata"}, Spec: v1alpha1.AddonSpec{Enabled: true}}
	if err := c.Create(ctx, addon); err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
	updateStatus(t, c, addon, func(a *v1alpha1.Addon) { a.Status.Conditions = readyConditions(ready) })
}

func setSetting(t testing.TB, c client.Client, key, value string) {
	t.Helper()
	ctx := context.Background()
	setting := &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}
	err := c.Get(ctx, client.ObjectKeyFromObject(setting), setting)
	setting.Spec.Value = value
	if apierrors.IsNotFound(err) {
		must(t, c.Create(ctx, setting))
	} else {
		must(t, err)
		must(t, c.Update(ctx, setting))
	}
	t.Cleanup(func() { _ = c.Delete(ctx, &v1alpha1.Setting{ObjectMeta: metav1.ObjectMeta{Name: key}}) })
}

func seedBuildPlatform(t testing.TB, c client.Client) string {
	t.Helper()
	ns := seedPlatform(t, c)
	createBuildSecrets(t, c, ns)
	setKataAddon(t, c, metav1.ConditionTrue)
	return ns
}

func createBuild(t testing.TB, c client.Client, namespace, name string, mutate func(*v1alpha1.BuildSpec)) v1alpha1.Build {
	t.Helper()
	build := fixtureBuild(namespace, name)
	mutate(&build.Spec)
	createObject(t, c, &build)
	return build
}

func noBuildMutation(*v1alpha1.BuildSpec) {}

func reconcileBuild(t *testing.T, r *BuildReconciler, namespace, name string) ctrl.Result {
	t.Helper()
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKey{Namespace: namespace, Name: name}})
	must(t, err)
	return result
}

func getBuild(t *testing.T, c client.Client, namespace, name string) v1alpha1.Build {
	t.Helper()
	var build v1alpha1.Build
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &build))
	return build
}

func getJob(t *testing.T, c client.Client, namespace, name string) batchv1.Job {
	t.Helper()
	var job batchv1.Job
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &job))
	return job
}

func requireNoJob(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	var job batchv1.Job
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: name}, &job); !apierrors.IsNotFound(err) {
		t.Fatalf("job %s must not exist: %v", name, err)
	}
}

func requireBuildState(t *testing.T, build v1alpha1.Build, phase, reason string) {
	t.Helper()
	cond := meta.FindStatusCondition(build.Status.Conditions, v1alpha1.ConditionReady)
	if build.Status.Phase != phase || build.Status.Reason != reason || cond == nil || cond.Reason != reason {
		t.Fatalf("build state %s %s, ready %+v; want %s %s", build.Status.Phase, build.Status.Reason, cond, phase, reason)
	}
}

func completeJob(t *testing.T, c client.Client, namespace, name string) {
	t.Helper()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	updateStatus(t, c, job, func(j *batchv1.Job) {
		started := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
		done := metav1.NewTime(fixedNow.Add(-time.Minute))
		j.Status.StartTime, j.Status.CompletionTime = &started, &done
		j.Status.Succeeded = 1
		j.Status.Ready = ptr.To(int32(0))
		j.Status.Terminating = ptr.To(int32(0))
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue, LastTransitionTime: done},
			{Type: batchv1.JobComplete, Status: corev1.ConditionTrue, LastTransitionTime: done},
		}
	})
}

func failJob(t *testing.T, c client.Client, namespace, name, reason string) {
	t.Helper()
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	updateStatus(t, c, job, func(j *batchv1.Job) {
		started := metav1.NewTime(fixedNow.Add(-5 * time.Minute))
		failed := metav1.NewTime(fixedNow.Add(-time.Minute))
		j.Status.StartTime = &started
		j.Status.Failed = 1
		j.Status.Ready = ptr.To(int32(0))
		j.Status.Terminating = ptr.To(int32(0))
		j.Status.Conditions = []batchv1.JobCondition{
			{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue, Reason: reason, LastTransitionTime: failed},
			{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: reason, Message: "the job failed", LastTransitionTime: failed},
		}
	})
}

func ensureBuildJob(t testing.TB, c client.Client, namespace, build string) batchv1.Job {
	t.Helper()
	ctx := context.Background()
	var job batchv1.Job
	err := c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: build}, &job)
	if err == nil {
		return job
	}
	if !apierrors.IsNotFound(err) {
		t.Fatal(err)
	}
	var owner v1alpha1.Build
	must(t, c.Get(ctx, client.ObjectKey{Namespace: namespace, Name: build}, &owner))
	job = batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: build, OwnerReferences: []metav1.OwnerReference{BuildOwnerRef(owner)}},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "x", Image: "registry.test/x:1"}},
		}}},
	}
	must(t, c.Create(ctx, &job))
	return job
}

func createServiceAccount(t testing.TB, c client.Client, namespace string) {
	t.Helper()
	err := c.Create(context.Background(), &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: "default"}})
	if err != nil && !apierrors.IsAlreadyExists(err) {
		t.Fatal(err)
	}
}

func jobOwnerRef(job batchv1.Job) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: ptr.To(true), BlockOwnerDeletion: ptr.To(true)}
}

func ownedBuildPod(t testing.TB, c client.Client, namespace, name, build string) corev1.Pod {
	t.Helper()
	createServiceAccount(t, c, namespace)
	job := ensureBuildJob(t, c, namespace, build)
	pod := podOf(namespace, name, build)
	pod.Labels = BuildSelectorLabels(build)
	pod.OwnerReferences = []metav1.OwnerReference{jobOwnerRef(job)}
	createObject(t, c, &pod)
	return pod
}

func forgedBuildPod(t testing.TB, c client.Client, namespace, name, build string) corev1.Pod {
	t.Helper()
	createServiceAccount(t, c, namespace)
	pod := podOf(namespace, name, build)
	pod.Labels = BuildSelectorLabels(build)
	createObject(t, c, &pod)
	return pod
}

func TestBuildReconcileCreatesTheJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "shop", noBuildMutation)
	result := reconcileBuild(t, newBuildReconciler(c), ns, "shop")
	if result.RequeueAfter == 0 {
		t.Fatal("a build in progress must be observed again")
	}
	build := getBuild(t, c, ns, "shop")
	job := getJob(t, c, ns, "shop")
	owner := metav1.GetControllerOf(&job)
	if owner == nil || owner.Kind != "Build" || owner.UID != build.UID || owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion {
		t.Fatalf("owner %+v", owner)
	}
	if job.Labels[v1alpha1.LabelKind] != "Build" || job.Labels[v1alpha1.LabelProject] != "web" || job.Labels[v1alpha1.LabelTenant] != "acme" {
		t.Fatalf("labels %v", job.Labels)
	}
	if *job.Spec.ActiveDeadlineSeconds != 1800 || *job.Spec.TTLSecondsAfterFinished != 3600 || *job.Spec.BackoffLimit != 0 {
		t.Fatalf("job limits %d %d %d", *job.Spec.ActiveDeadlineSeconds, *job.Spec.TTLSecondsAfterFinished, *job.Spec.BackoffLimit)
	}
	pod := job.Spec.Template
	if pod.Annotations["v1.multus-cni.io/default-network"] != ns+"/default" || *pod.Spec.RuntimeClassName != "kata" {
		t.Fatalf("pod template %+v", pod)
	}
	if pod.Spec.InitContainers[0].Image != "alpine/git:v2.54.0" || pod.Spec.Containers[0].Image != "moby/buildkit:v0.31.2" {
		t.Fatalf("images %s %s", pod.Spec.InitContainers[0].Image, pod.Spec.Containers[0].Image)
	}
	managed := false
	for _, entry := range job.ManagedFields {
		managed = managed || entry.Manager == v1alpha1.ContainerFieldManager
	}
	if !managed {
		t.Fatal("the job has no bedrock-container field manager")
	}
	requireBuildState(t, build, v1alpha1.BuildPhasePending, ReasonStarting)
	if build.Status.JobName != "shop" || build.Status.ObservedGeneration != build.Generation {
		t.Fatalf("status %+v", build.Status)
	}
	var secrets corev1.SecretList
	must(t, c.List(context.Background(), &secrets, client.InNamespace(ns)))
	if len(secrets.Items) != 2 {
		t.Fatalf("the controller must not create a Secret: %d", len(secrets.Items))
	}
	var configMaps corev1.ConfigMapList
	must(t, c.List(context.Background(), &configMaps, client.InNamespace(ns), client.MatchingLabels{v1alpha1.LabelKind: "Build"}))
	if len(configMaps.Items) != 0 {
		t.Fatalf("no mirrors, no ConfigMap: %d", len(configMaps.Items))
	}
}

func TestBuildReconcileRendersTheMirrorConfigMapOwnedByTheBuild(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	setSetting(t, c, SettingBuildMirrors, "docker.io=registry.test/mirror-docker")
	createBuild(t, c, ns, "mirrored", noBuildMutation)
	reconcileBuild(t, newBuildReconciler(c), ns, "mirrored")
	var configMap corev1.ConfigMap
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "mirrored-buildkitd"}, &configMap))
	owner := metav1.GetControllerOf(&configMap)
	if owner == nil || owner.Kind != "Build" || owner.Name != "mirrored" || *owner.BlockOwnerDeletion != true {
		t.Fatalf("owner %+v", owner)
	}
	if configMap.Data["buildkitd.toml"] != "[registry.\"docker.io\"]\n  mirrors = [\"registry.test/mirror-docker\"]\n" {
		t.Fatalf("data %v", configMap.Data)
	}
	job := getJob(t, c, ns, "mirrored")
	found := false
	for _, volume := range job.Spec.Template.Spec.Volumes {
		found = found || (volume.ConfigMap != nil && volume.ConfigMap.Name == "mirrored-buildkitd")
	}
	if !found {
		t.Fatalf("volumes %+v", job.Spec.Template.Spec.Volumes)
	}
}

func TestBuildChildrenCarryTheOwnerReferenceThatTheCollectorFollows(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	setSetting(t, c, SettingBuildMirrors, "docker.io=registry.test/mirror-docker")
	build := createBuild(t, c, ns, "doomed", noBuildMutation)
	reconcileBuild(t, newBuildReconciler(c), ns, "doomed")
	job := getJob(t, c, ns, "doomed")
	var configMap corev1.ConfigMap
	must(t, c.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: "doomed-buildkitd"}, &configMap))
	for _, child := range []client.Object{&job, &configMap} {
		owners := child.GetOwnerReferences()
		if len(owners) != 1 || owners[0].Kind != "Build" || owners[0].Name != "doomed" || owners[0].UID != getBuild(t, c, ns, "doomed").UID {
			t.Errorf("%T owners %+v", child, owners)
		}
	}
	must(t, c.Delete(context.Background(), &build))
	var gone v1alpha1.Build
	if err := c.Get(context.Background(), client.ObjectKeyFromObject(&build), &gone); !apierrors.IsNotFound(err) {
		t.Fatalf("the build must be deleted: %v", err)
	}
	if result := reconcileBuild(t, newBuildReconciler(c), ns, "doomed"); result.RequeueAfter != 0 {
		t.Fatalf("a deleted build needs no new look: %+v", result)
	}
}

func TestBuildTimeoutRules(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	r := newBuildReconciler(c)

	createBuild(t, c, ns, "long", func(s *v1alpha1.BuildSpec) { s.Timeout = &metav1.Duration{Duration: 3 * time.Hour} })
	reconcileBuild(t, r, ns, "long")
	requireBuildState(t, getBuild(t, c, ns, "long"), v1alpha1.BuildPhasePending, ReasonInvalidSpec)
	requireNoJob(t, c, ns, "long")

	createBuild(t, c, ns, "tiny", func(s *v1alpha1.BuildSpec) { s.Timeout = &metav1.Duration{Duration: time.Millisecond} })
	reconcileBuild(t, r, ns, "tiny")
	requireBuildState(t, getBuild(t, c, ns, "tiny"), v1alpha1.BuildPhasePending, ReasonInvalidSpec)
	requireNoJob(t, c, ns, "tiny")

	createBuild(t, c, ns, "ninety", func(s *v1alpha1.BuildSpec) { s.Timeout = &metav1.Duration{Duration: 90 * time.Minute} })
	reconcileBuild(t, r, ns, "ninety")
	if got := *getJob(t, c, ns, "ninety").Spec.ActiveDeadlineSeconds; got != 5400 {
		t.Fatalf("deadline %d", got)
	}

	setSetting(t, c, SettingBuildMaxTimeout, "1h")
	createBuild(t, c, ns, "above-max", func(s *v1alpha1.BuildSpec) { s.Timeout = &metav1.Duration{Duration: 90 * time.Minute} })
	reconcileBuild(t, r, ns, "above-max")
	requireBuildState(t, getBuild(t, c, ns, "above-max"), v1alpha1.BuildPhasePending, ReasonInvalidSpec)
	requireNoJob(t, c, ns, "above-max")

	setSetting(t, c, SettingBuildDefaultTimeout, "10m")
	createBuild(t, c, ns, "default", noBuildMutation)
	reconcileBuild(t, r, ns, "default")
	if got := *getJob(t, c, ns, "default").Spec.ActiveDeadlineSeconds; got != 600 {
		t.Fatalf("default deadline %d", got)
	}
}

type withoutRuntimeClasses struct{ client.Client }

func (w withoutRuntimeClasses) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if _, isClass := obj.(*nodev1.RuntimeClass); isClass {
		return apierrors.NewNotFound(nodev1.Resource("runtimeclasses"), key.Name)
	}
	return w.Client.Get(ctx, key, obj, opts...)
}

func TestBuildGatesStartNoJob(t *testing.T) {
	c, _ := startEnv(t)
	cases := []struct {
		name   string
		reason string
		prep   func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build)
	}{
		{"subnet missing", ReasonSubnetNotFound, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			b := fixtureBuild(ns, "gated")
			b.Spec.Network.SubnetRef = "absent"
			return c, b
		}},
		{"subnet not ready", ReasonSubnetNotReady, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			createSubnet(t, c, ns, "slow", metav1.ConditionFalse)
			b := fixtureBuild(ns, "gated")
			b.Spec.Network.SubnetRef = "slow"
			return c, b
		}},
		{"push secret missing", ReasonSecretNotFound, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			b := fixtureBuild(ns, "gated")
			b.Spec.Output.PushSecretRef = "absent"
			return c, b
		}},
		{"git secret missing", ReasonSecretNotFound, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			b := fixtureBuild(ns, "gated")
			b.Spec.Source.Git.SecretRef = "absent"
			return c, b
		}},
		{"push secret of the wrong type", ReasonSecretTypeInvalid, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "opaque"}, Data: map[string][]byte{"a": []byte("b")}})
			b := fixtureBuild(ns, "gated")
			b.Spec.Output.PushSecretRef = "opaque"
			return c, b
		}},
		{"git secret without the token", ReasonSecretTypeInvalid, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			createObject(t, c, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "no-token"}, Data: map[string][]byte{"username": []byte("robot")}})
			b := fixtureBuild(ns, "gated")
			b.Spec.Source.Git.SecretRef = "no-token"
			return c, b
		}},
		{"kata addon not ready", ReasonKataUnavailable, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			setKataAddon(t, c, metav1.ConditionFalse)
			t.Cleanup(func() { setKataAddon(t, c, metav1.ConditionTrue) })
			return c, fixtureBuild(ns, "gated")
		}},
		{"runtime class missing", ReasonKataUnavailable, func(t *testing.T, c client.Client, ns string) (client.Client, v1alpha1.Build) {
			return withoutRuntimeClasses{c}, fixtureBuild(ns, "gated")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ns := seedBuildPlatform(t, c)
			reading, build := tc.prep(t, c, ns)
			createObject(t, c, &build)
			reconcileBuild(t, newBuildReconciler(reading), ns, "gated")
			got := getBuild(t, c, ns, "gated")
			requireBuildState(t, got, v1alpha1.BuildPhasePending, tc.reason)
			requireNoJob(t, c, ns, "gated")
			if got.Status.JobName != "" {
				t.Fatalf("jobName %q", got.Status.JobName)
			}
			if cond := meta.FindStatusCondition(got.Status.Conditions, v1alpha1.ConditionReady); cond.Status != metav1.ConditionFalse || cond.Message == "" {
				t.Fatalf("ready %+v", cond)
			}
		})
	}
}

func TestBuildStartsWhenTheMissingSecretAppears(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedPlatform(t, c)
	setKataAddon(t, c, metav1.ConditionTrue)
	createBuild(t, c, ns, "late", noBuildMutation)
	r := newBuildReconciler(c)
	result := reconcileBuild(t, r, ns, "late")
	requireBuildState(t, getBuild(t, c, ns, "late"), v1alpha1.BuildPhasePending, ReasonSecretNotFound)
	if result.RequeueAfter == 0 {
		t.Fatal("a gated build must be tried again")
	}
	createBuildSecrets(t, c, ns)
	reconcileBuild(t, r, ns, "late")
	getJob(t, c, ns, "late")
	requireBuildState(t, getBuild(t, c, ns, "late"), v1alpha1.BuildPhasePending, ReasonStarting)
}

func TestBuildOutsideAProjectStartsNoJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := "plain-" + uniqueNamespaceSuffix()
	createObject(t, c, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: ns}})
	createBuild(t, c, ns, "lost", noBuildMutation)
	reconcileBuild(t, newBuildReconciler(c), ns, "lost")
	requireBuildState(t, getBuild(t, c, ns, "lost"), v1alpha1.BuildPhasePending, ReasonNotInProject)
	requireNoJob(t, c, ns, "lost")
}

func TestBuildReportsSuccessAndStaysFinal(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "ok", noBuildMutation)
	r := newBuildReconciler(c)
	reconcileBuild(t, r, ns, "ok")
	completeJob(t, c, ns, "ok")
	if result := reconcileBuild(t, r, ns, "ok"); result.RequeueAfter != 0 {
		t.Fatalf("a finished build needs no new look: %+v", result)
	}
	done := getBuild(t, c, ns, "ok")
	requireBuildState(t, done, v1alpha1.BuildPhaseSucceeded, ReasonSucceeded)
	if done.Status.Image != "registry.test/acme/shop:1" || done.Status.StartedAt == nil || done.Status.FinishedAt == nil {
		t.Fatalf("status %+v", done.Status)
	}
	if cond := meta.FindStatusCondition(done.Status.Conditions, v1alpha1.ConditionReady); cond.Status != metav1.ConditionTrue {
		t.Fatalf("ready %+v", cond)
	}

	must(t, c.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "ok"}}, client.PropagationPolicy(metav1.DeletePropagationBackground)))
	reconcileBuild(t, r, ns, "ok")
	requireNoJob(t, c, ns, "ok")
	after := getBuild(t, c, ns, "ok")
	requireBuildState(t, after, v1alpha1.BuildPhaseSucceeded, ReasonSucceeded)
	if !after.Status.FinishedAt.Equal(done.Status.FinishedAt) {
		t.Fatal("the finish time of a finished build never changes")
	}
}

func TestBuildFinishedWithAGoneJobMakesNoSecondJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	for _, phase := range []string{v1alpha1.BuildPhaseSucceeded, v1alpha1.BuildPhaseFailed} {
		name := "final-" + strings.ToLower(phase)
		build := createBuild(t, c, ns, name, noBuildMutation)
		updateStatus(t, c, &build, func(b *v1alpha1.Build) { b.Status.Phase, b.Status.Reason, b.Status.JobName = phase, "Test", name })
		result := reconcileBuild(t, newBuildReconciler(c), ns, name)
		requireNoJob(t, c, ns, name)
		if result.RequeueAfter != 0 {
			t.Errorf("%s: result %+v", phase, result)
		}
		if got := getBuild(t, c, ns, name); got.Status.Phase != phase || got.Status.Reason != "Test" {
			t.Errorf("%s: status %+v", phase, got.Status)
		}
	}
	fresh := createBuild(t, c, ns, "final-with-no-name", noBuildMutation)
	updateStatus(t, c, &fresh, func(b *v1alpha1.Build) { b.Status.Phase = v1alpha1.BuildPhaseFailed })
	reconcileBuild(t, newBuildReconciler(c), ns, "final-with-no-name")
	requireNoJob(t, c, ns, "final-with-no-name")
}

func TestBuildTimeoutAndFailureReasons(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	r := newBuildReconciler(c)
	for name, want := range map[string]string{"deadline": ReasonTimeout, "broken": ReasonFailed} {
		createBuild(t, c, ns, name, noBuildMutation)
		reconcileBuild(t, r, ns, name)
		jobReason := "BackoffLimitExceeded"
		if name == "deadline" {
			jobReason = "DeadlineExceeded"
		}
		failJob(t, c, ns, name, jobReason)
		reconcileBuild(t, r, ns, name)
		got := getBuild(t, c, ns, name)
		requireBuildState(t, got, v1alpha1.BuildPhaseFailed, want)
		if got.Status.FinishedAt == nil {
			t.Errorf("%s: no finish time", name)
		}
	}
}

func TestBuildStuckPodFailsTheBuildAndStopsTheJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "stuck", noBuildMutation)
	r := newBuildReconciler(c)
	reconcileBuild(t, r, ns, "stuck")
	pod := ownedBuildPod(t, c, ns, "stuck-abc", "stuck")
	updateStatus(t, c, &pod, func(p *corev1.Pod) {
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "git-clone", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "back-off pulling"}}}}
	})
	reconcileBuild(t, r, ns, "stuck")
	requireBuildState(t, getBuild(t, c, ns, "stuck"), v1alpha1.BuildPhasePending, ReasonStarting)
	getJob(t, c, ns, "stuck")

	later := &BuildReconciler{Client: c, Now: func() time.Time { return time.Now().Add(3 * time.Minute) }}
	reconcileBuild(t, later, ns, "stuck")
	failed := getBuild(t, c, ns, "stuck")
	requireBuildState(t, failed, v1alpha1.BuildPhaseFailed, ReasonImagePullFailed)
	requireNoJob(t, c, ns, "stuck")
	if failed.Status.FinishedAt == nil {
		t.Fatal("no finish time")
	}
}

func TestBuildWhoseJobDisappearsFailsAfterAMinute(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "vanish", noBuildMutation)
	at := fixedNow
	r := &BuildReconciler{Client: c, Now: func() time.Time { return at }}
	reconcileBuild(t, r, ns, "vanish")
	must(t, c.Delete(context.Background(), &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "vanish"}}, client.PropagationPolicy(metav1.DeletePropagationBackground)))

	reconcileBuild(t, r, ns, "vanish")
	requireBuildState(t, getBuild(t, c, ns, "vanish"), v1alpha1.BuildPhasePending, ReasonStarting)
	requireNoJob(t, c, ns, "vanish")
	at = fixedNow.Add(30 * time.Second)
	reconcileBuild(t, r, ns, "vanish")
	requireBuildState(t, getBuild(t, c, ns, "vanish"), v1alpha1.BuildPhasePending, ReasonStarting)
	at = fixedNow.Add(2 * time.Minute)
	reconcileBuild(t, r, ns, "vanish")
	requireBuildState(t, getBuild(t, c, ns, "vanish"), v1alpha1.BuildPhaseFailed, ReasonJobMissing)
	requireNoJob(t, c, ns, "vanish")
}

func TestBuildNeverRewritesARunningJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "steady", noBuildMutation)
	r := newBuildReconciler(c)
	reconcileBuild(t, r, ns, "steady")
	before := getJob(t, c, ns, "steady")
	setSetting(t, c, SettingBuildBuildKitImage, "moby/buildkit:v9.9.9")
	setSetting(t, c, SettingBuildMirrors, "docker.io=registry.test/mirror-docker")
	reconcileBuild(t, r, ns, "steady")
	after := getJob(t, c, ns, "steady")
	if after.ResourceVersion != before.ResourceVersion || after.Spec.Template.Spec.Containers[0].Image != "moby/buildkit:v0.31.2" {
		t.Fatalf("the job changed: %s to %s, image %s", before.ResourceVersion, after.ResourceVersion, after.Spec.Template.Spec.Containers[0].Image)
	}
}

func TestBuildLeavesAForeignJobAlone(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	foreign := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "taken"},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "x", Image: "registry.test/x:1"}},
		}}},
	}
	createObject(t, c, foreign)
	createBuild(t, c, ns, "taken", noBuildMutation)
	reconcileBuild(t, newBuildReconciler(c), ns, "taken")
	requireBuildState(t, getBuild(t, c, ns, "taken"), v1alpha1.BuildPhasePending, ReasonNameConflict)
	if got := getJob(t, c, ns, "taken"); got.Spec.Template.Spec.Containers[0].Name != "x" {
		t.Fatal("the foreign job was changed")
	}
}

func TestBuildReconcileLeavesAnAbsentBuildAlone(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	if result := reconcileBuild(t, newBuildReconciler(c), ns, "absent"); result.RequeueAfter != 0 {
		t.Fatalf("result %+v", result)
	}
}

func TestBuildReconcileCreatesABuildpacksJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	setSetting(t, c, SettingBuildMirrors, "docker.io=registry.test/mirror-docker")
	createBuild(t, c, ns, "cnb", func(s *v1alpha1.BuildSpec) { s.Type = v1alpha1.BuildTypeBuildpacks })
	reconcileBuild(t, newBuildReconciler(c), ns, "cnb")
	job := getJob(t, c, ns, "cnb")
	pod := job.Spec.Template.Spec
	if len(pod.InitContainers) != 5 || pod.Containers[0].Name != "exporter" || pod.Containers[0].Image != "paketobuildpacks/builder-jammy-base:0.4.640" {
		t.Fatalf("pod %+v", pod)
	}
	var configMaps corev1.ConfigMapList
	must(t, c.List(context.Background(), &configMaps, client.InNamespace(ns), client.MatchingLabels{v1alpha1.LabelKind: "Build"}))
	if len(configMaps.Items) != 0 {
		t.Fatalf("a buildpacks build has no mirror ConfigMap: %d", len(configMaps.Items))
	}
	requireBuildState(t, getBuild(t, c, ns, "cnb"), v1alpha1.BuildPhasePending, ReasonStarting)
}

func TestBuildForgedPodChangesNeitherTheStatusNorTheJob(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "target", noBuildMutation)
	r := newBuildReconciler(c)
	reconcileBuild(t, r, ns, "target")
	forged := forgedBuildPod(t, c, ns, "target-forged", "target")
	updateStatus(t, c, &forged, func(p *corev1.Pod) {
		p.Status.Phase = corev1.PodRunning
		p.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "git-clone", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff", Message: "forged"}}}}
	})
	later := &BuildReconciler{Client: c, Now: func() time.Time { return time.Now().Add(10 * time.Minute) }}
	reconcileBuild(t, later, ns, "target")
	build := getBuild(t, c, ns, "target")
	requireBuildState(t, build, v1alpha1.BuildPhasePending, ReasonStarting)
	getJob(t, c, ns, "target")
}

func TestBuildPodOfAnotherBuildsJobIsNotCounted(t *testing.T) {
	c, _ := startEnv(t)
	ns := seedBuildPlatform(t, c)
	createBuild(t, c, ns, "mine", noBuildMutation)
	createBuild(t, c, ns, "other", noBuildMutation)
	r := newBuildReconciler(c)
	reconcileBuild(t, r, ns, "mine")
	reconcileBuild(t, r, ns, "other")
	foreign := ownedBuildPod(t, c, ns, "other-pod", "other")
	relabeled := foreign.DeepCopy()
	relabeled.Labels = BuildSelectorLabels("mine")
	must(t, c.Update(context.Background(), relabeled))
	updateStatus(t, c, relabeled, func(p *corev1.Pod) { p.Status.Phase = corev1.PodRunning })
	reconcileBuild(t, r, ns, "mine")
	requireBuildState(t, getBuild(t, c, ns, "mine"), v1alpha1.BuildPhasePending, ReasonStarting)
}
