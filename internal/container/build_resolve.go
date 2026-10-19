package container

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/addons"
	"github.com/cloudyfolks-io/bedrock/internal/network"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

const (
	SettingBuildGitImage       = "build.git-image"
	SettingBuildBuildKitImage  = "build.buildkit-image"
	SettingBuildBuilderImage   = "build.builder-image"
	SettingBuildRunImage       = "build.run-image"
	SettingBuildDefaultTimeout = "build.default-timeout"
	SettingBuildMaxTimeout     = "build.max-timeout"
	SettingBuildJobTTL         = "build.job-ttl"
	SettingBuildMirrors        = "build.registry-mirrors"
)

type BuildResolver struct {
	Client  client.Client
	Secrets client.Reader
}

type buildStep func(ctx context.Context, b v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error)

func (r BuildResolver) Resolve(ctx context.Context, b v1alpha1.Build, labels map[string]string) (BuildInput, *Gate, error) {
	in := BuildInput{Build: b, Labels: labels}
	steps := []buildStep{r.stepSettings, r.stepTimeout, r.stepSubnet, r.stepDefaultGroup, r.stepSecrets, r.stepKata}
	for _, step := range steps {
		next, gate, err := step(ctx, b, in)
		if err != nil || gate != nil {
			return in, gate, err
		}
		in = next
	}
	return in, nil, nil
}

func (r BuildResolver) settingValue(ctx context.Context, key string) (string, error) {
	var setting v1alpha1.Setting
	err := r.Client.Get(ctx, client.ObjectKey{Name: key}, &setting)
	if err != nil && !notFound(err) {
		return "", err
	}
	if setting.Spec.Value != "" {
		return setting.Spec.Value, nil
	}
	return catalogDefault(key), nil
}

func catalogDefault(key string) string {
	definition, _ := settings.Lookup(key)
	return definition.Default
}

func (r BuildResolver) durationSetting(ctx context.Context, key string) (time.Duration, *Gate, error) {
	value, err := r.settingValue(ctx, key)
	if err != nil {
		return 0, nil, err
	}
	parsed, parseErr := time.ParseDuration(value)
	if parseErr != nil || parsed < time.Second {
		return 0, gateOf(ReasonInvalidSpec, "the Setting %s is not a valid duration", key), nil
	}
	return parsed, nil, nil
}

func (r BuildResolver) stepSettings(ctx context.Context, _ v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	images := []struct {
		key  string
		into *string
	}{
		{SettingBuildGitImage, &in.GitImage},
		{SettingBuildBuildKitImage, &in.BuildKitImage},
		{SettingBuildBuilderImage, &in.BuilderImage},
		{SettingBuildRunImage, &in.RunImage},
	}
	for _, image := range images {
		value, err := r.settingValue(ctx, image.key)
		if err != nil {
			return in, nil, err
		}
		*image.into = value
	}
	ttl, gate, err := r.durationSetting(ctx, SettingBuildJobTTL)
	if err != nil || gate != nil {
		return in, gate, err
	}
	in.JobTTL = ttl
	raw, err := r.settingValue(ctx, SettingBuildMirrors)
	if err != nil {
		return in, nil, err
	}
	in.Mirrors, in.MirrorProblems = settings.ParseRegistryMirrorsLenient(raw)
	return in, nil, nil
}

func (r BuildResolver) stepTimeout(ctx context.Context, b v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	maximum, gate, err := r.durationSetting(ctx, SettingBuildMaxTimeout)
	if err != nil || gate != nil {
		return in, gate, err
	}
	timeout := time.Duration(0)
	if b.Spec.Timeout != nil {
		timeout = b.Spec.Timeout.Duration
		if timeout < time.Second {
			return in, gateOf(ReasonInvalidSpec, "the timeout must be at least 1s"), nil
		}
	} else if timeout, gate, err = r.durationSetting(ctx, SettingBuildDefaultTimeout); err != nil || gate != nil {
		return in, gate, err
	}
	if timeout > maximum {
		return in, gateOf(ReasonInvalidSpec, "the timeout %s is above the Setting %s of %s", timeout, SettingBuildMaxTimeout, maximum), nil
	}
	in.Timeout = timeout
	return in, nil, nil
}

func (r BuildResolver) stepSubnet(ctx context.Context, b v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	name := b.Spec.Network.SubnetRef
	_, dns, gate, err := Resolver{Client: r.Client}.resolveSubnet(ctx, b.Namespace, name)
	if err != nil || gate != nil {
		return in, gate, err
	}
	if len(dns) == 0 {
		return in, gateOf(ReasonNoDNSServers, "the subnet %q has no DNS servers, and the Settings network.dns-servers and network.services.resolver are empty, so the build would use the cluster DNS", name), nil
	}
	in.NIC = network.NIC{Namespace: b.Namespace, Subnet: name}
	in.DNSServers = dns
	return in, nil, nil
}

func (r BuildResolver) stepDefaultGroup(ctx context.Context, b v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	gate, err := Resolver{Client: r.Client}.defaultGroupGate(ctx, b.Namespace)
	return in, gate, err
}

func (r BuildResolver) secretReader() client.Reader {
	if r.Secrets != nil {
		return r.Secrets
	}
	return r.Client
}

func (r BuildResolver) stepSecrets(ctx context.Context, b v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	push := b.Spec.Output.PushSecretRef
	pushSecret, gate, err := r.secret(ctx, b.Namespace, push, "push")
	if err != nil || gate != nil {
		return in, gate, err
	}
	if pushSecret.Type != corev1.SecretTypeDockerConfigJson || len(pushSecret.Data[corev1.DockerConfigJsonKey]) == 0 {
		return in, gateOf(ReasonSecretTypeInvalid, "the push secret %q must have the type %s and the key %s", push, corev1.SecretTypeDockerConfigJson, corev1.DockerConfigJsonKey), nil
	}
	git := b.Spec.Source.Git.SecretRef
	if git == "" {
		return in, nil, nil
	}
	gitSecret, gate, err := r.secret(ctx, b.Namespace, git, "git")
	if err != nil || gate != nil {
		return in, gate, err
	}
	if len(gitSecret.Data[gitUsernameKey]) == 0 || len(gitSecret.Data[gitTokenKey]) == 0 {
		return in, gateOf(ReasonSecretTypeInvalid, "the git secret %q must have the keys %s and %s", git, gitUsernameKey, gitTokenKey), nil
	}
	return in, nil, nil
}

func (r BuildResolver) secret(ctx context.Context, namespace, name, purpose string) (corev1.Secret, *Gate, error) {
	var secret corev1.Secret
	err := r.secretReader().Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, &secret)
	if notFound(err) {
		return secret, gateOf(ReasonSecretNotFound, "the %s secret %q does not exist", purpose, name), nil
	}
	return secret, nil, err
}

func (r BuildResolver) stepKata(ctx context.Context, _ v1alpha1.Build, in BuildInput) (BuildInput, *Gate, error) {
	err := r.Client.Get(ctx, client.ObjectKey{Name: BuildRuntimeClass}, &nodev1.RuntimeClass{})
	if notFound(err) {
		return in, kataGate("the RuntimeClass %q does not exist", BuildRuntimeClass), nil
	}
	if err != nil {
		return in, nil, err
	}
	var addon v1alpha1.Addon
	err = r.Client.Get(ctx, client.ObjectKey{Name: addons.Kata}, &addon)
	if notFound(err) || (err == nil && !meta.IsStatusConditionTrue(addon.Status.Conditions, v1alpha1.ConditionReady)) {
		return in, kataGate("the Addon %q is not ready", addons.Kata), nil
	}
	return in, nil, err
}

func kataGate(format string, args ...any) *Gate {
	return gateOf(ReasonKataUnavailable, "%s: a build runs a privileged container and needs a Kata microVM, which needs a host with KVM", fmt.Sprintf(format, args...))
}
