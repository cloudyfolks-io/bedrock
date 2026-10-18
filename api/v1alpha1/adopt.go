package v1alpha1

import (
	"fmt"
	"regexp"
	"slices"
)

var cephNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func AdoptPoolNameProblem(pools AdoptPools) string {
	names := []struct{ field, name string }{{"block", pools.Block}, {"filesystem", pools.Filesystem}, {"objectStore", pools.ObjectStore}}
	for _, entry := range names {
		if entry.name != "" && !cephNamePattern.MatchString(entry.name) {
			return fmt.Sprintf("storage.adopt.pools.%s is not a valid Ceph name", entry.field)
		}
	}
	return ""
}

func AdoptHostDevices(adopt *AdoptSpec, node string) []string {
	if adopt == nil {
		return nil
	}
	for _, host := range adopt.Hosts {
		if host.Node == node {
			return slices.Clone(host.Devices)
		}
	}
	return nil
}
