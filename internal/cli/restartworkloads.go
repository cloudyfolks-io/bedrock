package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/cloudyfolks-labs/bedrock/internal/agent"
	"github.com/cloudyfolks-labs/bedrock/internal/host"
)

func restartWorkloadsCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("restart-workloads", flag.ContinueOnError)
	flags.SetOutput(stderr)
	root := flags.String("root", "/", "host root that holds var/lib/k0s")
	timeout := flags.Duration("timeout", agent.K0sRestartTimeout, "time for the API to answer, and again for the restarts")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if _, err := os.Stat(filepath.Join(*root, "var/lib/k0s/pki/admin.conf")); err != nil {
		return fail(stderr, fmt.Errorf("run this on a controller: %w", err))
	}
	deps := agent.Deps{Exec: host.RealExec{}, Root: *root, K0sTimeout: *timeout, K0sPoll: agent.K0sRestartPoll, ProbeTimeout: agent.FactProbeTimeout}
	return restartWorkloads(context.Background(), deps, stdout, stderr)
}

func restartWorkloads(ctx context.Context, deps agent.Deps, stdout, stderr io.Writer) int {
	restarted, err := agent.RestartPlatformWorkloads(ctx, deps)
	fmt.Fprintf(stdout, "workloads restarted: %d\n", restarted)
	if err != nil {
		return fail(stderr, err)
	}
	return 0
}
