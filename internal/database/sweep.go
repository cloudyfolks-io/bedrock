package database

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/cloudyfolks-io/bedrock/internal/platform"
)

const sweepInterval = 10 * time.Minute

type Sweeper struct {
	Client client.Client
}

func (s *Sweeper) Start(ctx context.Context) error {
	ticker := time.NewTicker(sweepInterval)
	defer ticker.Stop()
	for {
		s.sweep(ctx)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (s *Sweeper) NeedLeaderElection() bool {
	return true
}

func (s *Sweeper) sweep(ctx context.Context) {
	orphans, err := s.Sweep(ctx)
	if err != nil {
		logf.FromContext(ctx).Info("database orphan sweep failed", "error", err.Error())
		return
	}
	for _, orphan := range orphans {
		logf.FromContext(ctx).Info("orphan database object", "kind", orphan.GetKind(), "name", orphan.GetName())
	}
}

func (s *Sweeper) Sweep(ctx context.Context) ([]*unstructured.Unstructured, error) {
	cfg, err := LoadConfig(ctx, s.Client)
	if err != nil || !cfg.Enabled {
		return nil, err
	}
	return platform.SweepOrphans(ctx, s.Client, EngineGVKs(), OwnerExists(s.Client), cfg.SweeperDryRun, client.InNamespace(PlatformNamespace))
}
