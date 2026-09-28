package cli

import (
	"fmt"
	"io"
	"sort"
)

type Command func(args []string, stdout, stderr io.Writer) int

var Version = "dev"

func Commands() map[string]Command {
	return map[string]Command{
		"version":           versionCommand,
		"operator":          operatorCommand,
		"release":           releaseCommand,
		"bundle":            bundleCommand,
		"init":              initCommand,
		"join":              joinCommand,
		"token":             tokenCommand,
		"agent":             agentCommand,
		"upgrade":           upgradeCommand,
		"restart-workloads": restartWorkloadsCommand,
	}
}

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stdout)
		return 2
	}
	if isHelpFlag(args[0]) {
		printUsage(stdout)
		return 0
	}
	command, ok := Commands()[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		return 2
	}
	return command(args[1:], stdout, stderr)
}

func versionCommand(_ []string, stdout, _ io.Writer) int {
	fmt.Fprintf(stdout, "bedrock %s\n", Version)
	return 0
}

func isHelpFlag(arg string) bool {
	return arg == "-h" || arg == "--help" || arg == "help"
}

func printUsage(w io.Writer) {
	names := make([]string, 0, len(Commands()))
	for name := range Commands() {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "usage: bedrock <command> [args]")
	for _, name := range names {
		fmt.Fprintf(w, "  %s\n", name)
	}
}
