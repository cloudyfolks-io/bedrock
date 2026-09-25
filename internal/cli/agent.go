package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-labs/bedrock/internal/agent"
	"github.com/cloudyfolks-labs/bedrock/internal/depot"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
	"github.com/cloudyfolks-labs/bedrock/internal/hostconfig"
	"github.com/cloudyfolks-labs/bedrock/internal/inventory"
	"github.com/cloudyfolks-labs/bedrock/internal/operator"
	"github.com/cloudyfolks-labs/bedrock/internal/pkgmgr"
)

type agentOptions struct {
	kubeconfig string
	node       string
	root       string
	interval   time.Duration
}

type agentDeps struct {
	Exec host.Exec
	OSID string
	Load func(path string) (client.WithWatch, time.Time, error)
	Run  func(ctx context.Context, c client.WithWatch, deps agent.Deps) error
}

func agentCommand(args []string, _, stderr io.Writer) int {
	flags := flag.NewFlagSet("agent", flag.ContinueOnError)
	flags.SetOutput(stderr)
	var o agentOptions
	flags.StringVar(&o.kubeconfig, "kubeconfig", filepath.Join("/var/lib/k0s", "kubelet.conf"), "kubelet kubeconfig with the node identity")
	flags.StringVar(&o.node, "node", "", "node name, default is the lowercase hostname")
	flags.StringVar(&o.root, "root", "/", "host root")
	flags.DurationVar(&o.interval, "interval", 60*time.Second, "reconcile interval")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if o.interval <= 0 {
		fmt.Fprintln(stderr, "agent: --interval must be greater than zero")
		return 2
	}
	if o.node == "" {
		name, err := os.Hostname()
		if err != nil {
			return fail(stderr, err)
		}
		o.node = strings.ToLower(name)
	}
	id, _, err := host.ReadOSRelease(filepath.Join(o.root, "etc", "os-release"))
	if err != nil {
		return fail(stderr, err)
	}
	scheme, err := operator.Scheme()
	if err != nil {
		return fail(stderr, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	load := func(path string) (client.WithWatch, time.Time, error) { return agent.LoadClient(path, scheme) }
	deps := agentDeps{
		Exec: host.RealExec{},
		OSID: id,
		Load: load,
		Run:  agent.Run,
	}
	go serveDepot(ctx, load, o, stderr)
	if err := runAgent(ctx, o, deps, stderr); err != nil {
		return fail(stderr, err)
	}
	return 0
}

func runAgent(ctx context.Context, o agentOptions, deps agentDeps, stderr io.Writer) error {
	family, err := pkgmgr.Detect(deps.OSID)
	if err != nil {
		fmt.Fprintf(stderr, "agent: %v, package steps will fail\n", err)
	}
	loopDeps := agent.Deps{
		Exec:      deps.Exec,
		Root:      o.root,
		Node:      o.node,
		Now:       time.Now,
		Interval:  o.interval,
		Inventory: inventory.Gather,
		Apply:     hostconfig.Apply,
		Packages:  pkgmgr.Manager{Exec: deps.Exec, Family: family, Root: o.root},
		Version:   Version,
		Hostname:  os.Hostname,
		FreeBytes: host.FreeBytes,
		HTTP:      &http.Client{},
	}
	for {
		changed, err := runUntilKubeconfigChanges(ctx, o, deps, loopDeps, stderr)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
	}
}

func runUntilKubeconfigChanges(ctx context.Context, o agentOptions, deps agentDeps, loopDeps agent.Deps, stderr io.Writer) (bool, error) {
	c, loaded, err := deps.Load(o.kubeconfig)
	if err != nil {
		return false, err
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- deps.Run(runCtx, c, loopDeps) }()
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			cancel()
			reportExit(stderr, <-done)
			return false, nil
		case err := <-done:
			if err != nil {
				return false, err
			}
			if ctx.Err() != nil {
				return false, nil
			}
			return false, errors.New("agent loop exited")
		case <-ticker.C:
			info, err := os.Stat(o.kubeconfig)
			if err != nil || info.ModTime().Equal(loaded) {
				continue
			}
			cancel()
			reportExit(stderr, <-done)
			return true, nil
		}
	}
}

func reportExit(stderr io.Writer, err error) {
	if err != nil {
		fmt.Fprintf(stderr, "agent: %v\n", err)
	}
}

func serveDepot(ctx context.Context, load func(string) (client.WithWatch, time.Time, error), o agentOptions, stderr io.Writer) {
	ticker := time.NewTicker(o.interval)
	defer ticker.Stop()
	for {
		ip, err := depotIP(ctx, load, o)
		if err == nil {
			if err := depot.Serve(ctx, ip, o.root); err != nil {
				fmt.Fprintf(stderr, "agent: depot: %v\n", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func depotIP(ctx context.Context, load func(string) (client.WithWatch, time.Time, error), o agentOptions) (string, error) {
	c, _, err := load(o.kubeconfig)
	if err != nil {
		return "", err
	}
	return agent.NodeInternalIP(ctx, c, o.node)
}
