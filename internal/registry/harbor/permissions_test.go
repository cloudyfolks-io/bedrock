package harbor

import (
	"slices"
	"testing"
)

func TestPullIsOnlyRepositoryPull(t *testing.T) {
	access, err := AccessFor(PermissionPull)
	if err != nil || !slices.Equal(access, []Access{{"repository", "pull"}}) {
		t.Fatalf("access %v %v", access, err)
	}
}

func TestPushPullAddsPush(t *testing.T) {
	access, err := AccessFor(PermissionPushPull)
	if err != nil || !slices.Equal(access, []Access{{"repository", "pull"}, {"repository", "push"}}) {
		t.Fatalf("access %v %v", access, err)
	}
}

func TestManageListsAndDeletesContentAndManagesRetention(t *testing.T) {
	access, err := AccessFor(PermissionManage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []Access{
		{"repository", "list"}, {"repository", "pull"}, {"repository", "push"}, {"repository", "delete"},
		{"artifact", "list"}, {"artifact", "read"}, {"artifact", "delete"},
		{"tag", "list"}, {"tag", "create"}, {"tag", "delete"},
		{"tag-retention", "list"}, {"tag-retention", "read"}, {"tag-retention", "create"}, {"tag-retention", "update"}, {"tag-retention", "delete"},
	} {
		if !slices.Contains(access, want) {
			t.Errorf("manage lacks %v", want)
		}
	}
	for _, denied := range access {
		if denied.Resource == "robot" || denied.Resource == "member" || denied.Resource == "project" {
			t.Errorf("manage holds %v", denied)
		}
	}
}

func TestAnUnknownPermissionIsAnError(t *testing.T) {
	if _, err := AccessFor("admin"); err == nil {
		t.Fatal("no error")
	}
}

func TestEveryPermissionHasNoDuplicates(t *testing.T) {
	for _, permission := range []string{PermissionPull, PermissionPushPull, PermissionManage} {
		access, _ := AccessFor(permission)
		seen := map[Access]bool{}
		for _, item := range access {
			if seen[item] {
				t.Errorf("%s repeats %v", permission, item)
			}
			seen[item] = true
		}
	}
}
