package release

import "k8s.io/apimachinery/pkg/util/version"

func IsDowngrade(running, to string) bool {
	from, err := version.ParseSemantic(running)
	if err != nil {
		return false
	}
	target, err := version.ParseSemantic(to)
	if err != nil {
		return false
	}
	return target.LessThan(from)
}
