package settings

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/cloudyfolks-io/bedrock/internal/dnsname"
)

const (
	minRegistryReplicas   = 1
	maxRegistryReplicas   = 5
	minRegistryDBReplicas = 1
	maxRegistryDBReplicas = 3
	proxyCachePrefix      = "mirror-"
	gcCronFields          = 6
	maxDefaultQuotaGB     = 1000000
)

var (
	harborAdapterTypes = []string{
		"harbor", "docker-hub", "docker-registry", "huawei-SWR", "google-gcr", "aws-ecr", "azure-acr",
		"ali-acr", "jfrog-artifactory", "quay", "gitlab", "dtr", "tencent-tcr", "github-ghcr", "volcengine-cr",
	}
	robotPrefixPattern  = regexp.MustCompile(`^[A-Za-z0-9$_-]{1,32}$`)
	proxyCacheName      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,29}$`)
	registryNameEntry   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,28}[a-z0-9])?$`)
	registryPrefixEntry = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,28}$`)
)

func registryDefinitions() []Definition {
	return []Definition{
		{"registry.enabled", "false", "Initial state of the registry addon. Change the Addon object after install.", boolean},
		{"registry.host", "", "DNS name of the registry. The addon waits until it is set.", domainName},
		{"registry.aliases", "", "Comma list of extra DNS names that serve the registry next to registry.host.", entryList("DNS name", dnsname.Valid)},
		{"registry.admin-host", "", "DNS name of the Harbor portal. Empty keeps the portal off the public routes.", domainName},
		{"registry.admin-allow-cidrs", "", "Comma list of IPv4 networks that may open the portal on registry.admin-host.", cidrListValue},
		{"registry.replicas", "2", "Replicas of the Harbor core, registry and jobservice, from 1 to 5.", intBetween(minRegistryReplicas, maxRegistryReplicas)},
		{"registry.hold", "false", "While true, the addon makes the database and the Secrets but keeps harbor-core and harbor-jobservice at zero replicas, so an administrator can restore an older database first. Set false to start them.", boolean},
		{"registry.db-instances", "2", "Instances of the Harbor database, from 1 to 3.", intBetween(minRegistryDBReplicas, maxRegistryDBReplicas)},
		{"registry.storage-class", "block", "StorageClass of the Harbor database and the Harbor Redis volume.", storageClassName},
		{"registry.bucket", "bedrock-registry", "Name of the bucket that holds the image blobs.", bucketNameValue},
		{"registry.http-proxy", "", "HTTP proxy that Harbor uses to reach upstream registries for the pull-through caches. Empty means no proxy.", httpURL},
		{"registry.robot-prefix", "robot$", "Prefix of the login name of every robot account.", robotPrefix},
		{"registry.gc-cron", "0 0 3 * * *", "Garbage collection schedule of Harbor, as a cron expression with six fields and seconds first.", gcCron},
		{"registry.default-quota-gb", "5", "Storage quota in GB of a Registry that sets no quotaGB. 0 means no limit.", intBetween(0, maxDefaultQuotaGB)},
		{"registry.reserved-names", "library,admin,registry,api,docs,system,internal,platform,builders", "Comma list of registry names that tenants cannot use. A user of the group bedrock-admins may use them. Empty keeps the default.", entryList("registry name", registryNameEntry.MatchString)},
		{"registry.reserved-prefixes", "mirror-", "Comma list of registry name prefixes that tenants cannot use. A user of the group bedrock-admins may use them. Empty keeps the default.", entryList("registry name prefix", registryPrefixEntry.MatchString)},
		{"registry.webhook-blocked-cidrs", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,100.64.0.0/10,169.254.0.0/16,127.0.0.0/8", "Comma list of IPv4 networks that the Harbor jobservice cannot reach, so a registry webhook cannot call into the cluster. Keep the pod network, the service network and the link-local range in it. The node addresses are always blocked. Empty keeps the default.", cidrListValue},
		{"registry.proxy-caches", "", "JSON list of pull-through caches. Each entry has name (starts with mirror-), type (a Harbor adapter such as docker-hub or docker-registry) and url (https).", proxyCaches},
	}
}

func storageClassName(v string) error {
	if v == "" || len(validation.IsDNS1123Subdomain(v)) == 0 {
		return nil
	}
	return fmt.Errorf("value %q is not a valid StorageClass name", v)
}

func robotPrefix(v string) error {
	if v == "" || robotPrefixPattern.MatchString(v) {
		return nil
	}
	return fmt.Errorf("value %q must have 1 to 32 letters, digits, $, _ or -", v)
}

func gcCron(v string) error {
	if v == "" {
		return nil
	}
	fields := strings.Fields(v)
	if len(fields) != gcCronFields {
		return fmt.Errorf("value %q must have %d fields", v, gcCronFields)
	}
	for i, field := range fields {
		if err := cronField(field, gcCronRanges[i]); err != nil {
			return fmt.Errorf("value %q: %w", v, err)
		}
	}
	return nil
}

type cronRange struct {
	name     string
	min, max int
	names    []string
}

var gcCronRanges = [gcCronFields]cronRange{
	{"second", 0, 59, nil},
	{"minute", 0, 59, nil},
	{"hour", 0, 23, nil},
	{"day of month", 1, 31, nil},
	{"month", 1, 12, []string{"JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC"}},
	{"day of week", 0, 7, []string{"SUN", "MON", "TUE", "WED", "THU", "FRI", "SAT"}},
}

func cronField(field string, allowed cronRange) error {
	for _, item := range strings.Split(field, ",") {
		if err := cronItem(item, allowed); err != nil {
			return fmt.Errorf("%s field %q: %w", allowed.name, field, err)
		}
	}
	return nil
}

func cronItem(item string, allowed cronRange) error {
	span, step, stepped := strings.Cut(item, "/")
	if stepped {
		if n, err := strconv.Atoi(step); err != nil || n < 1 {
			return fmt.Errorf("the step must be a positive number")
		}
	}
	if span == "*" || span == "?" {
		return nil
	}
	low, high, ranged := strings.Cut(span, "-")
	first, err := cronValue(low, allowed)
	if err != nil {
		return err
	}
	if !ranged {
		return nil
	}
	last, err := cronValue(high, allowed)
	if err != nil {
		return err
	}
	if first > last {
		return fmt.Errorf("the range runs backwards")
	}
	return nil
}

func cronValue(text string, allowed cronRange) (int, error) {
	if index := slices.Index(allowed.names, strings.ToUpper(text)); index >= 0 {
		return index + allowed.min, nil
	}
	n, err := strconv.Atoi(text)
	if err != nil || n < allowed.min || n > allowed.max {
		return 0, fmt.Errorf("%q is not between %d and %d", text, allowed.min, allowed.max)
	}
	return n, nil
}

type proxyCacheEntry struct {
	Name string `json:"name"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

func proxyCaches(v string) error {
	if v == "" {
		return nil
	}
	var entries []proxyCacheEntry
	if err := json.Unmarshal([]byte(v), &entries); err != nil {
		return fmt.Errorf("value is not a JSON list of caches: %w", err)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if err := validProxyCache(entry); err != nil {
			return err
		}
		if seen[entry.Name] {
			return fmt.Errorf("cache name %q appears twice", entry.Name)
		}
		seen[entry.Name] = true
	}
	return nil
}

func validProxyCache(entry proxyCacheEntry) error {
	if !strings.HasPrefix(entry.Name, proxyCachePrefix) || !proxyCacheName.MatchString(entry.Name) {
		return fmt.Errorf("cache name %q must start with %s and be a valid project name", entry.Name, proxyCachePrefix)
	}
	if !slices.Contains(harborAdapterTypes, entry.Type) {
		return fmt.Errorf("cache %q has type %q; use one of %s", entry.Name, entry.Type, strings.Join(harborAdapterTypes, ", "))
	}
	parsed, err := url.Parse(entry.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("cache %q needs an https URL with a host", entry.Name)
	}
	return nil
}
