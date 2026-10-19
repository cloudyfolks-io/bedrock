package settings

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"time"

	"k8s.io/apimachinery/pkg/api/resource"
)

func databaseDefinitions() []Definition {
	return []Definition{
		{"cnpg.enabled", "false", "Initial state of the cnpg addon. Change the Addon object after install.", boolean},
		{"database.enabled", "false", "Turns the Database kinds on. When false, no platform object exists and a Database shows the reason DatabaseDisabled.", boolean},
		{"database.capacity.cpu", "32", "CPU requests that all engine pods may hold together in the namespace bedrock-dbaas.", positiveQuantity},
		{"database.capacity.memory", "128Gi", "Memory requests that all engine pods may hold together in the namespace bedrock-dbaas.", positiveQuantity},
		{"database.capacity.pods", "150", "Pods that the namespace bedrock-dbaas may hold, from 1 to 100000.", intBetween(1, 100000)},
		{"database.max-utilization", "0.9", "Share of the capacity that a new Database may use, above 0 and at most 1. A create above it is refused with the reason CapacityExceeded.", floatBetween(0.01, 1)},
		{"database.sweeper-dry-run", "true", "When true, the orphan sweeper of the database objects only writes a log line. Set it to false after you checked the log.", boolean},
		{"database.provision-timeout", "30m", "Time after which a Database that is not ready becomes Failed.", duration(time.Minute)},
		{"database.backup.bucket", "bedrock-database-backups", "Bucket of the object store that holds the backups of all databases, one prefix for each database.", bucketNameValue},
		{"database.backup.retention-days", "7", "Days of backups and WAL archive that a database keeps, from 1 to 365.", intBetween(1, 365)},
		{"database.backup.retention-after-delete", "168h", "Time that the backups of a deleted database stay in the bucket before the sweeper removes them.", duration(0)},
		{"database.exec-timeout", "120s", "Time limit of one SQL script that the operator runs in an engine pod.", wholeSecondsBetween(5*time.Second, 10*time.Minute)},
	}
}

var bucketNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)

func bucketNameValue(v string) error {
	if v == "" || bucketNamePattern.MatchString(v) {
		return nil
	}
	return fmt.Errorf("value %q is not a bucket name of 3 to 63 lower-case letters, digits and dashes", v)
}

func positiveQuantity(v string) error {
	if v == "" {
		return nil
	}
	quantity, err := resource.ParseQuantity(v)
	if err != nil {
		return err
	}
	if quantity.Sign() <= 0 {
		return fmt.Errorf("quantity %q must be positive", v)
	}
	return nil
}

func floatBetween(minimum, maximum float64) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		parsed, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return err
		}
		if math.IsNaN(parsed) || parsed < minimum || parsed > maximum {
			return fmt.Errorf("value %q must be a number from %v to %v", v, minimum, maximum)
		}
		return nil
	}
}
