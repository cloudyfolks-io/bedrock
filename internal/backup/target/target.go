package target

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const defaultRegion = "us-east-1"

var (
	ErrEmpty   = errors.New("backup target is empty")
	bucketName = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
)

type Target struct{ Scheme, Endpoint, Bucket, Prefix, Region string }

func S3URL(t Target) string {
	return t.Scheme + "://" + t.Endpoint
}

func Parse(raw string) (Target, error) {
	if raw == "" {
		return Target{}, ErrEmpty
	}
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, errors.New("backup target is not a valid URL: check the host, port and path")
	}
	if err := checkShape(u); err != nil {
		return Target{}, err
	}
	bucket, prefix, err := splitPath(u.Path)
	if err != nil {
		return Target{}, err
	}
	scheme, region, err := readQuery(u.Query())
	if err != nil {
		return Target{}, err
	}
	return Target{Scheme: scheme, Endpoint: u.Host, Bucket: bucket, Prefix: prefix, Region: region}, nil
}

func checkShape(u *url.URL) error {
	switch {
	case u.Scheme == "nfs":
		return errors.New("nfs targets are not supported: Velero writes to S3-compatible object storage; use an s3:// target, for example an S3 gateway in front of the export")
	case u.Scheme != "s3":
		return fmt.Errorf("scheme must be s3, got %q", u.Scheme)
	case u.User != nil:
		return errors.New("credentials in the URL are not allowed")
	case u.Fragment != "" || strings.Contains(u.String(), "#"):
		return errors.New("fragment is not allowed")
	case u.Hostname() == "":
		return errors.New("endpoint is missing")
	}
	return checkPort(u.Port())
}

func checkPort(port string) error {
	if port == "" {
		return nil
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("port %q is out of range 1-65535", port)
	}
	return nil
}

func splitPath(path string) (string, string, error) {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(path, "/"), "/")
	if trimmed == "" {
		return "", "", errors.New("bucket is missing")
	}
	bucket, prefix, hasPrefix := strings.Cut(trimmed, "/")
	if !bucketName.MatchString(bucket) || strings.Contains(bucket, "..") {
		return "", "", fmt.Errorf("bucket name %q is not valid", bucket)
	}
	if hasPrefix && !validPrefix(prefix) {
		return "", "", fmt.Errorf("prefix %q is not valid", prefix)
	}
	return bucket, prefix, nil
}

func validPrefix(prefix string) bool {
	for _, segment := range strings.Split(prefix, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func readQuery(query url.Values) (string, string, error) {
	for key := range query {
		if key != "region" && key != "insecure" {
			return "", "", fmt.Errorf("unknown query key %q", key)
		}
	}
	region, scheme := defaultRegion, "https"
	if values, ok := query["region"]; ok {
		if values[0] == "" {
			return "", "", errors.New("region must not be empty")
		}
		region = values[0]
	}
	if values, ok := query["insecure"]; ok {
		if values[0] != "true" {
			return "", "", errors.New("insecure must be true when present")
		}
		scheme = "http"
	}
	return scheme, region, nil
}
