package release

import (
	"slices"
	"strings"
)

func forHelmEvents(group Group, events ...string) Group {
	result := Group{Name: group.Name, Order: group.Order}
	for _, obj := range group.Objects {
		hooks := obj.GetAnnotations()["helm.sh/hook"]
		if hooks == "" {
			if slices.Contains(events, "") {
				result.Objects = append(result.Objects, obj)
			}
			continue
		}
		for hook := range strings.SplitSeq(hooks, ",") {
			hook = strings.TrimSpace(hook)
			if hook != "" && slices.Contains(events, hook) {
				result.Objects = append(result.Objects, obj)
				break
			}
		}
	}
	return result
}
