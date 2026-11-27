package postgres

import (
	"regexp"

	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/database/exec"
)

const socketDir = "/controller/run"

var sqlstatePattern = regexp.MustCompile(`ERROR:\s+([0-9A-Z]{5}):`)

func Target(namespace, name string) exec.Target {
	return exec.Target{
		Namespace: namespace,
		Selector:  client.MatchingLabels{"cnpg.io/cluster": name, "cnpg.io/instanceRole": "primary"},
		Container: "postgres",
		Command:   []string{"env", "LC_MESSAGES=C", "psql", "-X", "-q", "-A", "-t", "-v", "ON_ERROR_STOP=1", "-v", "VERBOSITY=verbose", "-h", socketDir, "-U", "postgres", "-d", "postgres", "-F", "|"},
		Classify:  Classify,
	}
}

func Classify(stderr string) exec.Kind {
	if match := sqlstatePattern.FindStringSubmatch(stderr); match != nil {
		switch match[1] {
		case "42710", "42P04", "23505":
			return exec.KindExists
		case "42704":
			return exec.KindNotFound
		case "2BP01":
			return exec.KindOwnsObjects
		}
	}
	return exec.KindUnclassified
}
