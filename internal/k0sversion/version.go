package k0sversion

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const Any = "any"

var pattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+\+k0s\.[0-9]+$`)

func Valid(version string) bool {
	return pattern.MatchString(version)
}

func ParseAllowed(value string) ([]string, error) {
	fields := strings.Fields(value)
	if len(fields) == 1 && fields[0] == Any {
		return nil, nil
	}
	for _, field := range fields {
		if !Valid(field) {
			return nil, fmt.Errorf("%q is not a k0s version like v1.36.3+k0s.1", field)
		}
	}
	return fields, nil
}

func Allowed(allowed []string, version string) bool {
	if !Valid(version) {
		return false
	}
	return len(allowed) == 0 || slices.Contains(allowed, version)
}
