package v1alpha1

import "slices"

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
