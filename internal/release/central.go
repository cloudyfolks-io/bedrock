package release

import (
	"context"
	"maps"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/cloudyfolks-io/bedrock/internal/roles"
)

const (
	VarCentralNodes = "BEDROCK_CENTRAL_NODES"

	centralName = "ovn-central"
)

var centralTermsPath = []string{"spec", "template", "spec", "affinity", "nodeAffinity", "requiredDuringSchedulingIgnoredDuringExecution", "nodeSelectorTerms"}

type MasterNode struct {
	Name    string
	Address string
}

func MasterNodes(ctx context.Context, c client.Reader) ([]MasterNode, error) {
	var nodes corev1.NodeList
	if err := c.List(ctx, &nodes, client.MatchingLabels{roles.FabricRoleLabel: "master"}); err != nil {
		return nil, err
	}
	var masters []MasterNode
	for _, node := range nodes.Items {
		for _, addr := range node.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				masters = append(masters, MasterNode{Name: node.Name, Address: addr.Address})
				break
			}
		}
	}
	sort.Slice(masters, func(i, j int) bool { return masters[i].Address < masters[j].Address })
	return masters, nil
}

func WithMasters(vars map[string]string, nodes []MasterNode) map[string]string {
	out := maps.Clone(vars)
	addresses := make([]string, len(nodes))
	names := make([]string, len(nodes))
	for i, node := range nodes {
		addresses[i] = node.Address
		names[i] = node.Name
	}
	out[VarMasterIPs] = strings.Join(addresses, ",")
	out[VarCentralNodes] = strings.Join(names, ",")
	return out
}

func SetCentralTopology(bundle Bundle, vars map[string]string) Bundle {
	addresses := vars[VarMasterIPs]
	if addresses == "" {
		return bundle
	}
	replicas := int64(len(strings.Split(addresses, ",")))
	var names []string
	if vars[VarCentralNodes] != "" {
		names = strings.Split(vars[VarCentralNodes], ",")
	}
	out := bundle
	out.Groups = make([]Group, len(bundle.Groups))
	for i, group := range bundle.Groups {
		out.Groups[i] = group
		if group.Name != fabricGroup {
			continue
		}
		out.Groups[i].Objects = make([]*unstructured.Unstructured, len(group.Objects))
		for j, obj := range group.Objects {
			out.Groups[i].Objects[j] = withCentralTopology(obj, replicas, names)
		}
	}
	return out
}

func withCentralTopology(obj *unstructured.Unstructured, replicas int64, names []string) *unstructured.Unstructured {
	if obj.GetKind() != "Deployment" || obj.GetName() != centralName {
		return obj
	}
	copied := obj.DeepCopy()
	_ = unstructured.SetNestedField(copied.Object, replicas, "spec", "replicas")
	if len(names) > 0 {
		_ = unstructured.SetNestedSlice(copied.Object, pinnedTerms(copied, names), centralTermsPath...)
	}
	return copied
}

func pinnedTerms(obj *unstructured.Unstructured, names []string) []any {
	terms, _, _ := unstructured.NestedSlice(obj.Object, centralTermsPath...)
	if len(terms) == 0 {
		terms = []any{map[string]any{}}
	}
	var pinned []any
	for _, name := range names {
		field := []any{map[string]any{"key": "metadata.name", "operator": "In", "values": []any{name}}}
		for _, term := range terms {
			if fields, ok := term.(map[string]any); ok {
				next := maps.Clone(fields)
				next["matchFields"] = field
				pinned = append(pinned, next)
			}
		}
	}
	return pinned
}
