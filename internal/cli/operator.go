package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/cloudyfolks-io/bedrock/internal/operator"
)

func operatorCommand(args []string, _, stderr io.Writer) int {
	flags := flag.NewFlagSet("operator", flag.ContinueOnError)
	flags.SetOutput(stderr)
	releaseDir := flags.String("release-dir", defaultReleaseDir(), "release directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	ctrl.SetLogger(zap.New())
	cfg, err := ctrl.GetConfig()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	scheme, err := operator.Scheme()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err := operator.Run(ctrl.SetupSignalHandler(), cfg, scheme, operator.RunOptions{ReleaseDir: *releaseDir}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func defaultReleaseDir() string {
	if dir := os.Getenv("BEDROCK_RELEASE_DIR"); dir != "" {
		return dir
	}
	return "/release"
}
