package housekeeping

import (
	"context"
	"crypto/rand"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/release"
)

const (
	LeaseName     = "bedrock-authn-housekeeping"
	leaseDuration = 15 * time.Second
	renewDeadline = 10 * time.Second
	retryPeriod   = 2 * time.Second
)

func Run(ctx context.Context, cfg *rest.Config, c client.Client, identity string, interval time.Duration) error {
	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return err
	}
	lock := &resourcelock.LeaseLock{
		LeaseMeta:  metav1.ObjectMeta{Namespace: release.SystemNamespace, Name: LeaseName},
		Client:     clientset.CoordinationV1(),
		LockConfig: resourcelock.ResourceLockConfig{Identity: identity},
	}
	for ctx.Err() == nil {
		elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
			Lock:            lock,
			LeaseDuration:   leaseDuration,
			RenewDeadline:   renewDeadline,
			RetryPeriod:     retryPeriod,
			ReleaseOnCancel: true,
			Name:            LeaseName,
			Callbacks: leaderelection.LeaderCallbacks{
				OnStartedLeading: func(leading context.Context) { lead(leading, c, interval) },
				OnStoppedLeading: func() {},
			},
		})
		if err != nil {
			return err
		}
		elector.Run(ctx)
	}
	return nil
}

func lead(ctx context.Context, c client.Client, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		housekeep(ctx, c, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func housekeep(ctx context.Context, c client.Client, now time.Time) {
	logger := ctrl.LoggerFrom(ctx)
	if err := Rotate(ctx, c, rand.Reader, now); err != nil {
		logger.Error(err, "rotate signing keys")
	}
	if _, err := Sweep(ctx, c, now); err != nil {
		logger.Error(err, "sweep expired authn objects")
	}
}
