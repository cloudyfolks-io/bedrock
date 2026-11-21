package proxy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
	"github.com/cloudyfolks-io/bedrock/internal/tenancy"
)

func TestProjectNetworksUseTheCallersRealKubernetesPermissions(t *testing.T) {
	reader := startEnv(t)
	ctx := context.Background()
	project := projectNamed("network-home", "acme-network-home")
	outside := projectNamed("network-outside", "beta-network-outside")
	outside.Spec.TenantRef = "beta"
	for _, item := range []*v1alpha1.Project{project, outside} {
		status := item.Status
		if err := reader.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		item.Status = status
		if err := reader.Status().Update(ctx, item); err != nil {
			t.Fatal(err)
		}
	}
	alice := rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:alice"}
	bob := rbacv1.Subject{APIGroup: rbacv1.GroupName, Kind: "User", Name: "bedrock:bob"}
	projectRole := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: "network-home-viewers"}
	networkRole := rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "ClusterRole", Name: "network-home-owners"}
	createIfMissing(t,
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: project.Status.Namespace}},
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: projectRole.Name, Namespace: project.Status.Namespace},
			Rules: []rbacv1.PolicyRule{
				{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"virtualmachines"}, Verbs: []string{"list"}},
				{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"networks"}, Verbs: []string{"get"}, ResourceNames: []string{"network-home-hidden"}},
			},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: projectRole.Name, Namespace: project.Status.Namespace},
			RoleRef:    projectRole,
			Subjects:   []rbacv1.Subject{alice, bob},
		},
		&rbacv1.ClusterRole{
			ObjectMeta: metav1.ObjectMeta{Name: networkRole.Name},
			Rules:      []rbacv1.PolicyRule{{APIGroups: []string{v1alpha1.GroupVersion.Group}, Resources: []string{"networks"}, Verbs: []string{"get", "list", "watch"}, ResourceNames: []string{"network-home-main", "network-outside-shared"}}},
		},
		&rbacv1.ClusterRoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: networkRole.Name},
			RoleRef:    networkRole,
			Subjects:   []rbacv1.Subject{alice},
		},
		testNetwork("network-home-main", "acme"),
		testNetwork("network-home-hidden", "acme"),
		testNetwork("network-outside-shared", "beta"),
	)
	upstream, err := NewUpstream(testCfg)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.Handle("GET "+PathProjects+"/{project}/networks", ProjectNetworksHandler(upstream, reader))
	cases := []struct {
		name    string
		token   string
		project string
		status  int
		items   []string
	}{
		{"named owner", "token-alice", project.Name, http.StatusOK, []string{"network-home-main"}},
		{"project viewer", "token-bob", project.Name, http.StatusForbidden, nil},
		{"another tenant", "token-alice", outside.Name, http.StatusNotFound, nil},
		{"unknown project", "token-alice", "network-absent", http.StatusNotFound, nil},
		{"cluster admin", "token-admin", project.Name, http.StatusOK, []string{"network-home-hidden", "network-home-main"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := asCaller(mux, tc.token)
			path := PathProjects + "/" + tc.project + "/networks"
			var answer *httptest.ResponseRecorder
			for deadline := time.Now().Add(15 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
				answer = do(handler, httptest.NewRequest(http.MethodGet, path, nil))
				if answer.Code == tc.status {
					break
				}
			}
			if answer.Code != tc.status {
				t.Fatalf("status %d, want %d: %s", answer.Code, tc.status, answer.Body.String())
			}
			if tc.status != http.StatusOK {
				return
			}
			var list v1alpha1.NetworkList
			if err := json.Unmarshal(answer.Body.Bytes(), &list); err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, network := range list.Items {
				names = append(names, network.Name)
			}
			slices.Sort(names)
			if !slices.Equal(names, tc.items) {
				t.Fatalf("networks %v, want %v", names, tc.items)
			}
		})
	}
}

func TestProjectNetworkListRejectsOverflow(t *testing.T) {
	networks := make([]client.Object, 0, projectPage+1)
	for i := range projectPage + 1 {
		networks = append(networks, testNetwork("network-"+strconv.Itoa(i), "acme"))
	}
	items, err := listTenantNetworks(context.Background(), fakeReader(t, networks...), "acme")
	if len(items) != 0 || err != errNetworkInventoryLimit {
		t.Fatalf("got %d records and %v, want no partial list and the inventory limit error", len(items), err)
	}
}

func TestProjectNetworksRejectBlockedNamespacesBeforeAuthorization(t *testing.T) {
	api := newReviewAPI(t, func(authorizationv1.ResourceAttributes) bool { return true })
	upstream := api.upstream(t)
	for _, reason := range []string{tenancy.ReasonNamespaceConflict, tenancy.ReasonNamespaceTerminating} {
		t.Run(reason, func(t *testing.T) {
			project := projectWithBlockedNamespace("network-home", "acme-network-home", reason)
			reader := interceptor.NewClient(fakeReader(t), interceptor.Funcs{
				List: func(_ context.Context, _ client.WithWatch, objects client.ObjectList, _ ...client.ListOption) error {
					objects.(*v1alpha1.ProjectList).Items = []v1alpha1.Project{*project}
					return nil
				},
			})
			handler := asCaller(ProjectNetworksHandler(upstream, reader), "token-alice")
			request := httptest.NewRequest(http.MethodGet, PathProjects+"/network-home/networks", nil)
			request.SetPathValue("project", "network-home")
			response := do(handler, request)
			if response.Code != http.StatusNotFound || len(api.seen()) != 0 {
				t.Fatalf("status %d, upstream requests %d", response.Code, len(api.seen()))
			}
		})
	}
}

func TestProjectNetworkRequestHasABoundedDeadline(t *testing.T) {
	reader := interceptor.NewClient(fakeReader(t), interceptor.Funcs{
		List: func(ctx context.Context, _ client.WithWatch, _ client.ObjectList, _ ...client.ListOption) error {
			deadline, ok := ctx.Deadline()
			if remaining := time.Until(deadline); !ok || remaining <= 0 || remaining > networkInventoryTimeout {
				t.Fatalf("list deadline remaining %s, want a timeout within %s", remaining, networkInventoryTimeout)
			}
			return context.DeadlineExceeded
		},
	})
	handler := asCaller(ProjectNetworksHandler(nil, reader), "token-alice")
	request := httptest.NewRequest(http.MethodGet, PathProjects+"/network-home/networks", nil)
	request.SetPathValue("project", "network-home")
	if response := do(handler, request); response.Code != http.StatusBadGateway {
		t.Fatalf("status %d, want %d", response.Code, http.StatusBadGateway)
	}
}

func testNetwork(name, tenant string) *v1alpha1.Network {
	return &v1alpha1.Network{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Spec:       v1alpha1.NetworkSpec{TenantRef: tenant, CIDRs: []string{"10.100.0.0/16"}},
	}
}
