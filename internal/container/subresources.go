package container

import (
	"time"

	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/cloudyfolks-io/bedrock/internal/proxy"
)

const (
	subresourceKind      = "containers"
	buildSubresourceKind = "builds"
)

type Deps struct {
	Config  *rest.Config
	Now     func() time.Time
	LogPoll time.Duration
}

func NewDeps(cfg *rest.Config) Deps {
	return Deps{Config: cfg, Now: time.Now}
}

func (d Deps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func RegisterSubresources(reg *proxy.Subresources) error {
	cfg, err := ctrl.GetConfig()
	if err != nil {
		return err
	}
	return registerWith(reg, NewDeps(cfg))
}

func registerWith(reg *proxy.Subresources, d Deps) error {
	handlers := []struct {
		name    string
		handler proxy.SubresourceHandler
	}{
		{"logs", proxy.SubresourceHandler(Logs(d))},
		{"exec", proxy.SubresourceHandler(Exec(d))},
		{"restart", proxy.SubresourceHandler(Restart(d))},
	}
	for _, entry := range handlers {
		if err := reg.Register(subresourceKind, entry.name, entry.handler); err != nil {
			return err
		}
	}
	return reg.Register(buildSubresourceKind, "logs", proxy.SubresourceHandler(BuildLogs(d)))
}
