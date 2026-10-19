package container

import (
	"path"

	corev1 "k8s.io/api/core/v1"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

const (
	cnbPlatformAPI  = "0.7"
	cnbLifecycleDir = "/cnb/lifecycle/"
	cnbAnalyzedFile = layersDir + "/analyzed.toml"
	cnbGroupFile    = layersDir + "/group.toml"
	cnbPlanFile     = layersDir + "/plan.toml"
	cnbExporterName = "exporter"
	cnbAnalyzerName = "analyzer"
	cnbDetectorName = "detector"
	cnbRestorerName = "restorer"
	cnbBuilderName  = "builder"
	cnbCacheFlag    = "-cache-image="
)

type cnbPhase struct {
	name string
	auth func(v1alpha1.BuildSpec) bool
	args func(in BuildInput) []string
}

func always(v1alpha1.BuildSpec) bool { return true }
func never(v1alpha1.BuildSpec) bool  { return false }

func whenCached(spec v1alpha1.BuildSpec) bool { return spec.Cache.Enabled }

func cnbPhases() (inits []cnbPhase, exporter cnbPhase) {
	return []cnbPhase{
			{cnbAnalyzerName, always, analyzerArgs},
			{cnbDetectorName, never, detectorArgs},
			{cnbRestorerName, whenCached, restorerArgs},
			{cnbBuilderName, never, detectorArgs},
		},
		cnbPhase{cnbExporterName, always, exporterArgs}
}

func appDir(spec v1alpha1.BuildSpec) string {
	return path.Join(workspaceDir, valueOrDefault(spec.ContextPath, "."))
}

func cacheArg(spec v1alpha1.BuildSpec) []string {
	if !spec.Cache.Enabled {
		return nil
	}
	return []string{cnbCacheFlag + CacheImage(spec.Output.Image)}
}

func analyzerArgs(in BuildInput) []string {
	args := []string{"-layers=" + layersDir, "-analyzed=" + cnbAnalyzedFile}
	if in.RunImage != "" {
		args = append(args, "-run-image="+in.RunImage)
	}
	return append(args, in.Build.Spec.Output.Image)
}

func detectorArgs(in BuildInput) []string {
	return []string{"-layers=" + layersDir, "-app=" + appDir(in.Build.Spec), "-group=" + cnbGroupFile, "-plan=" + cnbPlanFile}
}

func restorerArgs(in BuildInput) []string {
	return append([]string{"-layers=" + layersDir}, cacheArg(in.Build.Spec)...)
}

func exporterArgs(in BuildInput) []string {
	args := []string{"-layers=" + layersDir, "-app=" + appDir(in.Build.Spec), "-analyzed=" + cnbAnalyzedFile, in.Build.Spec.Output.Image}
	return append(args, cacheArg(in.Build.Spec)...)
}

func cnbContainer(in BuildInput, phase cnbPhase) corev1.Container {
	mounts := []corev1.VolumeMount{workspaceMount(), {Name: layersVolume, MountPath: layersDir}}
	env := []corev1.EnvVar{{Name: "CNB_PLATFORM_API", Value: cnbPlatformAPI}}
	if phase.auth(in.Build.Spec) {
		mounts = append(mounts, corev1.VolumeMount{Name: pushSecretVolume, MountPath: dockerConfigDir, ReadOnly: true})
		env = append(env, corev1.EnvVar{Name: "DOCKER_CONFIG", Value: dockerConfigDir})
	}
	return corev1.Container{
		Name:            phase.name,
		Image:           in.BuilderImage,
		Command:         []string{cnbLifecycleDir + phase.name},
		Args:            phase.args(in),
		WorkingDir:      workspaceDir,
		Env:             env,
		VolumeMounts:    mounts,
		Resources:       tierOf(in.Build.Spec.Size).resources(),
		SecurityContext: nonRootContext(),

		TerminationMessagePolicy: corev1.TerminationMessageFallbackToLogsOnError,
	}
}

func buildpacksVolumes(in BuildInput) []corev1.Volume {
	tier := tierOf(in.Build.Spec.Size)
	return []corev1.Volume{
		scratchVolume(workspaceVolume, tier),
		scratchVolume(layersVolume, tier),
		pushSecretVolumeOf(in),
	}
}

func renderBuildpacksBuild(in BuildInput) (Rendered, error) {
	phases, exporter := cnbPhases()
	inits := []corev1.Container{gitCloneContainer(in)}
	for _, phase := range phases {
		inits = append(inits, cnbContainer(in, phase))
	}
	job, err := renderJob(in, inits, cnbContainer(in, exporter), buildpacksVolumes(in))
	if err != nil {
		return Rendered{}, err
	}
	return Rendered{Job: job}, nil
}
