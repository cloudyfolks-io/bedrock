package settings

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/cloudyfolks-io/bedrock/internal/dnsname"
)

const (
	minBuckets = 1
	maxBuckets = 10000
)

var (
	bucketNameEntry   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
	bucketPrefixEntry = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,61}$`)
)

func objectStorageDefinitions() []Definition {
	return []Definition{
		{"objectstorage.reserved-bucket-names", "admin", "Comma list of bucket names that tenants cannot use. A user of the group bedrock-admins may use them. Empty keeps the default.", entryList("bucket name", bucketNameEntry.MatchString)},
		{"objectstorage.reserved-bucket-prefixes", "", "Comma list of bucket name prefixes that tenants cannot use, for example git-,cloudyfolks-. A user of the group bedrock-admins may use them.", entryList("bucket name prefix", bucketPrefixEntry.MatchString)},
		{"objectstorage.cors-origins", "", "Comma list of web origins, like https://panel.example.com, that every bucket allows in its CORS rules next to the origins of its own spec.", entryList("web origin", isWebOrigin)},
		{"objectstorage.max-buckets", "100", "Buckets that one project may hold, from 1 to 10000. Buckets that exist stay when the value is lowered.", intBetween(minBuckets, maxBuckets)},
		{"objectstorage.public-host", "", "DNS name of the public HTTPS endpoint of object storage. Empty keeps the store inside the cluster.", domainName},
		{"objectstorage.public-aliases", "", "Comma list of extra DNS names that serve the public endpoint, next to objectstorage.public-host.", entryList("DNS name", dnsname.Valid)},
	}
}

func entryList(what string, valid func(string) bool) func(string) error {
	return func(v string) error {
		if v == "" {
			return nil
		}
		for _, entry := range strings.Split(v, ",") {
			if !valid(entry) {
				return fmt.Errorf("value %q is not a comma list of %s entries", v, what)
			}
		}
		return nil
	}
}

func isWebOrigin(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return false
	}
	return parsed.User == nil && (parsed.Path == "" || parsed.Path == "/") && parsed.RawQuery == "" && parsed.Fragment == ""
}
