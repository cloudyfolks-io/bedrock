package harbor

import "fmt"

const (
	PermissionPull     = "pull"
	PermissionPushPull = "pushPull"
	PermissionManage   = "manage"
)

type Access struct {
	Resource string `json:"resource"`
	Action   string `json:"action"`
}

var pullAccess = []Access{{"repository", "pull"}}

var pushPullAccess = append(append([]Access{}, pullAccess...), Access{"repository", "push"})

var manageAccess = []Access{
	{"repository", "list"}, {"repository", "pull"}, {"repository", "push"}, {"repository", "delete"},
	{"artifact", "list"}, {"artifact", "read"}, {"artifact", "delete"},
	{"tag", "list"}, {"tag", "create"}, {"tag", "delete"},
	{"tag-retention", "list"}, {"tag-retention", "read"}, {"tag-retention", "create"}, {"tag-retention", "update"}, {"tag-retention", "delete"},
}

func AccessFor(permission string) ([]Access, error) {
	switch permission {
	case PermissionPull:
		return append([]Access{}, pullAccess...), nil
	case PermissionPushPull:
		return append([]Access{}, pushPullAccess...), nil
	case PermissionManage:
		return append([]Access{}, manageAccess...), nil
	}
	return nil, fmt.Errorf("permission %q is not one of %s, %s, %s", permission, PermissionPull, PermissionPushPull, PermissionManage)
}
