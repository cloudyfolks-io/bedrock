package container

import (
	"fmt"
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	BuildKind         = "Build"
	BuildRuntimeClass = "kata"

	gitCloneContainerName = "git-clone"
	buildContainerName    = "build"

	workspaceVolume    = "workspace"
	layersVolume       = "layers"
	buildkitVolume     = "buildkit-state"
	pushSecretVolume   = "push-credentials"
	mirrorConfigVolume = "buildkitd-config"

	workspaceDir     = "/workspace"
	layersDir        = "/layers"
	buildkitStateDir = "/var/lib/buildkit"
	dockerConfigDir  = "/docker-config"
	buildkitdFile    = "buildkitd.toml"
	buildkitdPath    = "/etc/buildkit/" + buildkitdFile

	builderUser  int64 = 1001
	builderGroup int64 = 1000

	gitUsernameKey = "username"
	gitTokenKey    = "token"
	pushConfigKey  = corev1.DockerConfigJsonKey
	pushConfigFile = "config.json"
)

const gitCloneScript = `set -eu
export HOME=/tmp
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0=` + workspaceDir + `
if [ -n "${GIT_TOKEN:-}" ]; then
  GIT_BASIC_AUTH=$(printf '%s:%s' "$GIT_USERNAME" "$GIT_TOKEN" | base64 | tr -d '\n')
  export GIT_CONFIG_COUNT=2
  export GIT_CONFIG_KEY_1=http.extraHeader
  export GIT_CONFIG_VALUE_1="Authorization: Basic ${GIT_BASIC_AUTH}"
fi
git clone --no-checkout -- "$GIT_URL" ` + workspaceDir + `
git checkout --detach "$GIT_REVISION" --
`

type BuildInput struct {
	Build         v1alpha1.Build
	Labels        map[string]string
	NIC           network.NIC
	DNSServers    []string
	GitImage      string
	BuildKitImage string
	BuilderImage  string
	RunImage      string
	Timeout       time.Duration
	JobTTL        time.Duration
	Mirrors       []settings.RegistryMirror
}

type Rendered struct {
	Job       *unstructured.Unstructured
	ConfigMap *unstructured.Unstructured
}

func BuildSelectorLabels(name string) map[string]string {
	return map[string]string{v1alpha1.LabelKind: BuildKind, v1alpha1.LabelName: name}
}

func BuildOwnerLabels(name string, ns corev1.Namespace) (map[string]string, bool) {
	tenant, project := ns.Labels[v1alpha1.LabelTenant], ns.Labels[v1alpha1.LabelProject]
	if tenant == "" || project == "" {
		return nil, false
	}
	return v1alpha1.TenantLabels(BuildKind, name, tenant, project), true
}

func BuildOwnerRef(b v1alpha1.Build) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion:         v1alpha1.GroupVersion.String(),
		Kind:               BuildKind,
		Name:               b.Name,
		UID:                b.UID,
		Controller:         ptr.To(true),
		BlockOwnerDeletion: ptr.To(true),
	}
}

func MirrorConfigName(buildName string) string {
	return buildName + "-buildkitd"
}

func RenderBuild(in BuildInput) (Rendered, error) {
	switch in.Build.Spec.Type {
	case "", v1alpha1.BuildTypeDockerfile:
		return renderDockerfileBuild(in)
	}
	return Rendered{}, fmt.Errorf("the build type %q is not supported", in.Build.Spec.Type)
}

func renderDockerfileBuild(in BuildInput) (Rendered, error) {
	job, err := renderJob(in, []corev1.Container{gitCloneContainer(in)}, buildContainer(in), dockerfileVolumes(in))
	if err != nil {
		return Rendered{}, err
	}
	configMap, err := renderMirrorConfig(in)
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{Job: job, ConfigMap: configMap}, nil
}

func renderJob(in BuildInput, inits []corev1.Container, main corev1.Container, volumes []corev1.Volume) (*unstructured.Unstructured, error) {
	attachment := network.PodAttachment(in.NIC)
	job := batchv1.Job{
		TypeMeta: metav1.TypeMeta{APIVersion: "batch/v1", Kind: "Job"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            in.Build.Name,
			Namespace:       in.Build.Namespace,
			Labels:          maps.Clone(in.Labels),
			OwnerReferences: []metav1.OwnerReference{BuildOwnerRef(in.Build)},
		},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr.To(int32(0)),
			ActiveDeadlineSeconds:   ptr.To(int64(in.Timeout / time.Second)),
			TTLSecondsAfterFinished: ptr.To(int32(in.JobTTL / time.Second)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels:      mergedStrings(mergedStrings(in.Labels, BuildSelectorLabels(in.Build.Name)), attachment.Labels),
					Annotations: maps.Clone(attachment.Annotations),
				},
				Spec: buildPodSpec(in, inits, main, volumes),
			},
		},
	}
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&job)
	if err != nil {
		return nil, fmt.Errorf("convert the build job: %w", err)
	}
	obj := &unstructured.Unstructured{Object: body}
	removeGenerated(obj, "spec", "template", "metadata", "creationTimestamp")
	return obj, nil
}

func buildPodSpec(in BuildInput, inits []corev1.Container, main corev1.Container, volumes []corev1.Volume) corev1.PodSpec {
	pod := corev1.PodSpec{
		RestartPolicy:                corev1.RestartPolicyNever,
		AutomountServiceAccountToken: ptr.To(false),
		EnableServiceLinks:           ptr.To(false),
		RuntimeClassName:             ptr.To(BuildRuntimeClass),
		SecurityContext:              &corev1.PodSecurityContext{FSGroup: ptr.To(builderGroup)},
		InitContainers:               inits,
		Containers:                   []corev1.Container{main},
		Volumes:                      volumes,
		ImagePullSecrets:             []corev1.LocalObjectReference{{Name: in.Build.Spec.Output.PushSecretRef}},
	}
	if len(in.DNSServers) > 0 {
		pod.DNSPolicy = corev1.DNSNone
		pod.DNSConfig = &corev1.PodDNSConfig{Nameservers: slices.Clone(in.DNSServers)}
	}
	return pod
}

func nonRootContext() *corev1.SecurityContext {
	return &corev1.SecurityContext{
		RunAsNonRoot:             ptr.To(true),
		RunAsUser:                ptr.To(builderUser),
		RunAsGroup:               ptr.To(builderGroup),
		AllowPrivilegeEscalation: ptr.To(false),
	}
}

func workspaceMount() corev1.VolumeMount {
	return corev1.VolumeMount{Name: workspaceVolume, MountPath: workspaceDir}
}

func gitCloneContainer(in BuildInput) corev1.Container {
	git := in.Build.Spec.Source.Git
	env := []corev1.EnvVar{
		{Name: "GIT_URL", Value: git.URL},
		{Name: "GIT_REVISION", Value: git.Revision},
	}
	if git.SecretRef != "" {
		env = append(env, secretEnv("GIT_USERNAME", git.SecretRef, gitUsernameKey), secretEnv("GIT_TOKEN", git.SecretRef, gitTokenKey))
	}
	return corev1.Container{
		Name:            gitCloneContainerName,
		Image:           in.GitImage,
		Command:         []string{"sh", "-c", gitCloneScript},
		WorkingDir:      workspaceDir,
		Env:             env,
		VolumeMounts:    []corev1.VolumeMount{workspaceMount()},
		Resources:       tierOf(in.Build.Spec.Size).resources(),
		SecurityContext: nonRootContext(),
	}
}

func secretEnv(name, secret, key string) corev1.EnvVar {
	return corev1.EnvVar{Name: name, ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secret}, Key: key}}}
}

func buildContainer(in BuildInput) corev1.Container {
	return corev1.Container{
		Name:            buildContainerName,
		Image:           in.BuildKitImage,
		Command:         []string{"buildctl-daemonless.sh"},
		Args:            buildctlArgs(in.Build.Spec),
		Env:             buildkitEnv(in),
		VolumeMounts:    buildkitMounts(in),
		Resources:       tierOf(in.Build.Spec.Size).resources(),
		SecurityContext: &corev1.SecurityContext{Privileged: ptr.To(true), RunAsUser: ptr.To(int64(0))},
	}
}

func buildctlArgs(spec v1alpha1.BuildSpec) []string {
	dockerfile := valueOrDefault(spec.DockerfilePath, "Dockerfile")
	args := []string{
		"build",
		"--frontend", "dockerfile.v0",
		"--local", "context=" + path.Join(workspaceDir, valueOrDefault(spec.ContextPath, ".")),
		"--local", "dockerfile=" + path.Join(workspaceDir, path.Dir(dockerfile)),
		"--opt", "filename=" + path.Base(dockerfile),
		"--output", "type=image,name=" + spec.Output.Image + ",push=true",
	}
	if spec.Cache.Enabled {
		ref := CacheImage(spec.Output.Image)
		args = append(args,
			"--export-cache", "type=registry,ref="+ref+",mode=max",
			"--import-cache", "type=registry,ref="+ref,
		)
	}
	return args
}

func valueOrDefault(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func CacheImage(image string) string {
	slash := strings.LastIndex(image, "/")
	if colon := strings.Index(image[slash+1:], ":"); colon >= 0 {
		image = image[:slash+1+colon]
	}
	return image + ":cache"
}

func buildkitEnv(in BuildInput) []corev1.EnvVar {
	env := []corev1.EnvVar{{Name: "DOCKER_CONFIG", Value: dockerConfigDir}}
	if len(in.Mirrors) > 0 {
		env = append(env, corev1.EnvVar{Name: "BUILDKITD_FLAGS", Value: "--config " + buildkitdPath})
	}
	return env
}

func buildkitMounts(in BuildInput) []corev1.VolumeMount {
	mounts := []corev1.VolumeMount{
		workspaceMount(),
		{Name: pushSecretVolume, MountPath: dockerConfigDir, ReadOnly: true},
		{Name: buildkitVolume, MountPath: buildkitStateDir},
	}
	if len(in.Mirrors) > 0 {
		mounts = append(mounts, corev1.VolumeMount{Name: mirrorConfigVolume, MountPath: buildkitdPath, SubPath: buildkitdFile, ReadOnly: true})
	}
	return mounts
}

func scratchVolume(name string, tier buildTier) corev1.Volume {
	limit := tier.scratchLimit()
	return corev1.Volume{Name: name, VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{SizeLimit: &limit}}}
}

func pushSecretVolumeOf(in BuildInput) corev1.Volume {
	return corev1.Volume{Name: pushSecretVolume, VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{
		SecretName:  in.Build.Spec.Output.PushSecretRef,
		Items:       []corev1.KeyToPath{{Key: pushConfigKey, Path: pushConfigFile}},
		DefaultMode: ptr.To(int32(0o440)),
	}}}
}

func dockerfileVolumes(in BuildInput) []corev1.Volume {
	tier := tierOf(in.Build.Spec.Size)
	volumes := []corev1.Volume{
		scratchVolume(workspaceVolume, tier),
		scratchVolume(layersVolume, tier),
		pushSecretVolumeOf(in),
		scratchVolume(buildkitVolume, tier),
	}
	if len(in.Mirrors) > 0 {
		volumes = append(volumes, corev1.Volume{Name: mirrorConfigVolume, VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{
			LocalObjectReference: corev1.LocalObjectReference{Name: MirrorConfigName(in.Build.Name)},
		}}})
	}
	return volumes
}

func renderMirrorConfig(in BuildInput) (*unstructured.Unstructured, error) {
	if len(in.Mirrors) == 0 {
		return nil, nil
	}
	configMap := corev1.ConfigMap{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"},
		ObjectMeta: metav1.ObjectMeta{
			Name:            MirrorConfigName(in.Build.Name),
			Namespace:       in.Build.Namespace,
			Labels:          maps.Clone(in.Labels),
			OwnerReferences: []metav1.OwnerReference{BuildOwnerRef(in.Build)},
		},
		Data: map[string]string{buildkitdFile: BuildkitdConfig(in.Mirrors)},
	}
	body, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&configMap)
	if err != nil {
		return nil, fmt.Errorf("convert the mirror config: %w", err)
	}
	obj := &unstructured.Unstructured{Object: body}
	removeGenerated(obj)
	return obj, nil
}

func BuildkitdConfig(mirrors []settings.RegistryMirror) string {
	byUpstream := map[string][]string{}
	for _, mirror := range mirrors {
		byUpstream[mirror.Upstream] = append(byUpstream[mirror.Upstream], mirror.Target)
	}
	var out strings.Builder
	for _, upstream := range slices.Sorted(maps.Keys(byUpstream)) {
		targets := slices.Clone(byUpstream[upstream])
		slices.Sort(targets)
		quoted := make([]string, len(targets))
		for i, target := range targets {
			quoted[i] = fmt.Sprintf("%q", target)
		}
		fmt.Fprintf(&out, "[registry.%q]\n  mirrors = [%s]\n", upstream, strings.Join(quoted, ", "))
	}
	return out.String()
}
