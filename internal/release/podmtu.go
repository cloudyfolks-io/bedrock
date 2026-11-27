package release

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const (
	VarPodMTU = "BEDROCK_POD_MTU"

	fabricGroup      = "fabric"
	fabricCNIServer  = "cni-server"
	fabricMTUFlag    = "--mtu="
	fabricAnchorFlag = "--service-cluster-ip-range="
)

func WithPodMTU(vars map[string]string, mtu int32) map[string]string {
	out := make(map[string]string, len(vars)+1)
	for key, value := range vars {
		out[key] = value
	}
	if mtu > 0 {
		out[VarPodMTU] = strconv.Itoa(int(mtu))
	}
	return out
}

func SetPodMTU(bundle Bundle, vars map[string]string) (Bundle, error) {
	mtu := vars[VarPodMTU]
	if mtu == "" {
		return bundle, nil
	}
	out := bundle
	out.Groups = make([]Group, 0, len(bundle.Groups))
	patched := false
	for _, group := range bundle.Groups {
		if group.Name != fabricGroup {
			out.Groups = append(out.Groups, group)
			continue
		}
		objects := make([]*unstructured.Unstructured, 0, len(group.Objects))
		for _, obj := range group.Objects {
			next, ok, err := withAgentMTU(obj, mtu)
			if err != nil {
				return Bundle{}, err
			}
			patched = patched || ok
			objects = append(objects, next)
		}
		out.Groups = append(out.Groups, Group{Order: group.Order, Name: group.Name, Objects: objects})
	}
	if !patched {
		return Bundle{}, fmt.Errorf("pod MTU %s is set, but the release has no Fabric %s container", mtu, fabricCNIServer)
	}
	return out, nil
}

func withAgentMTU(obj *unstructured.Unstructured, mtu string) (*unstructured.Unstructured, bool, error) {
	if obj.GetKind() != "DaemonSet" {
		return obj, false, nil
	}
	copied := obj.DeepCopy()
	containers, found, err := unstructured.NestedSlice(copied.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return obj, false, err
	}
	patched := false
	for i, container := range containers {
		fields, ok := container.(map[string]any)
		if !ok || fields["name"] != fabricCNIServer {
			continue
		}
		args, _, err := unstructured.NestedStringSlice(fields, "args")
		if err != nil {
			return nil, false, err
		}
		next, err := argsWithMTU(args, mtu)
		if err != nil {
			return nil, false, fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
		}
		if err := unstructured.SetNestedStringSlice(fields, next, "args"); err != nil {
			return nil, false, err
		}
		containers[i] = fields
		patched = true
	}
	if !patched {
		return obj, false, nil
	}
	if err := unstructured.SetNestedSlice(copied.Object, containers, "spec", "template", "spec", "containers"); err != nil {
		return nil, false, err
	}
	return copied, true, nil
}

func argsWithMTU(args []string, mtu string) ([]string, error) {
	kept := slices.DeleteFunc(slices.Clone(args), func(arg string) bool { return strings.HasPrefix(arg, fabricMTUFlag) })
	anchor := slices.IndexFunc(kept, func(arg string) bool { return strings.HasPrefix(arg, fabricAnchorFlag) })
	if anchor < 0 {
		return nil, fmt.Errorf("no %s argument to place the MTU after", fabricAnchorFlag)
	}
	return slices.Insert(kept, anchor+1, fabricMTUFlag+mtu), nil
}
