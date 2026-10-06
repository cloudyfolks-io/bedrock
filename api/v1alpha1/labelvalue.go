package v1alpha1

import "strings"

const maxLabelValue = 63

func LabelValue(name string) string {
	return strings.Trim(name[:min(len(name), maxLabelValue)], "-_.")
}
