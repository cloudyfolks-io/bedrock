package operator

import (
	"context"
	"fmt"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/release"
	"github.com/cloudyfolks-io/bedrock/internal/settings"
)

func Scheme() (*runtime.Scheme, error) {
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1alpha1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	return scheme, nil
}

type RunOptions struct {
	ReleaseDir string
}

func managerOptions(scheme *runtime.Scheme) ctrl.Options {
	return ctrl.Options{
		Scheme:                  scheme,
		Metrics:                 metricsserver.Options{BindAddress: metricsAddress},
		LeaderElection:          true,
		LeaderElectionID:        "bedrock-operator",
		LeaderElectionNamespace: "bedrock-system",
		Client:                  client.Options{Cache: &client.CacheOptions{DisableFor: []client.Object{&corev1.ConfigMap{}, &corev1.Secret{}}}},
	}
}

func Run(ctx context.Context, cfg *rest.Config, scheme *runtime.Scheme, opts RunOptions) error {
	bundle, err := release.Load(os.DirFS(opts.ReleaseDir))
	if err != nil {
		return fmt.Errorf("load release from %s: %w", opts.ReleaseDir, err)
	}
	mgr, err := ctrl.NewManager(cfg, managerOptions(scheme))
	if err != nil {
		return err
	}
	podName, err := runningOperatorPodName()
	if err != nil {
		return fmt.Errorf("get operator pod name: %w", err)
	}
	bundle, err = runningOperatorBundle(ctx, mgr.GetAPIReader(), bundle, podName)
	if err != nil {
		return err
	}
	if err := setupControllers(mgr, cfg, bundle); err != nil {
		return err
	}
	if err := registerStatusMetrics(mgr.GetClient()); err != nil {
		return err
	}
	if err := mgr.Add(seeder{client: mgr.GetClient()}); err != nil {
		return err
	}
	return mgr.Start(ctx)
}

func runningOperatorPodName() (string, error) {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name, nil
	}
	if os.Getenv("KUBERNETES_SERVICE_HOST") == "" {
		return "", nil
	}
	return os.Hostname()
}

func runningOperatorBundle(ctx context.Context, reader client.Reader, bundle release.Bundle, podName string) (release.Bundle, error) {
	if podName == "" {
		return bundle, nil
	}
	key := client.ObjectKey{Namespace: release.SystemNamespace, Name: podName}
	var pod corev1.Pod
	if err := reader.Get(ctx, key, &pod); err != nil {
		return release.Bundle{}, fmt.Errorf("read operator pod %s: %w", key, err)
	}
	for _, container := range pod.Spec.Containers {
		if container.Name == operatorContainer {
			return release.WithRunningImage(bundle, container.Image)
		}
	}
	return release.Bundle{}, fmt.Errorf("pod %s has no container %s", key, operatorContainer)
}

func setupControllers(mgr ctrl.Manager, cfg *rest.Config, bundle release.Bundle) error {
	if err := (&SettingReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&HostReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader()}).SetupWithManager(mgr); err != nil {
		return err
	}
	exec, err := podExec(cfg)
	if err != nil {
		return err
	}
	if err := (&ClusterReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Bundle: bundle, Gates: release.Gates{}, Interval: 2 * time.Second, GroupTimeout: 30 * time.Minute, UpgradeInterval: 10 * time.Second, Exec: exec, Dial: dialTCP, Recorder: mgr.GetEventRecorderFor("bedrock-operator")}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&StorageTopologyReconciler{Client: mgr.GetClient()}).SetupWithManager(mgr); err != nil {
		return err
	}
	if err := (&AddonReconciler{Client: mgr.GetClient(), Bundle: bundle, Addons: DefaultAddons(), Interval: DefaultAddonInterval, ReadyInterval: DefaultAddonReadyInterval, Exec: exec}).SetupWithManager(mgr); err != nil {
		return err
	}
	return setupAreas(mgr, bundle)
}

type seeder struct {
	client client.Client
}

func (s seeder) Start(ctx context.Context) error {
	return settings.Seed(ctx, s.client)
}

func (s seeder) NeedLeaderElection() bool {
	return true
}
