package addons

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func ResolveValues(def Definition, given map[string]string) (map[string]string, error) {
	known := map[string]Value{}
	for _, value := range def.Values {
		known[value.Key] = value
	}
	for _, key := range sortedKeys(given) {
		if _, ok := known[key]; !ok {
			return nil, fmt.Errorf("unknown value %q for addon %s; known keys: %s", key, def.Name, knownKeys(def))
		}
	}
	resolved := map[string]string{}
	for _, value := range def.Values {
		resolved[value.Key] = value.Default
		if override, ok := given[value.Key]; ok {
			resolved[value.Key] = override
		}
		if err := value.Validate(resolved[value.Key]); err != nil {
			return nil, fmt.Errorf("value %q: %w", value.Key, err)
		}
	}
	return resolved, nil
}

func knownKeys(def Definition) string {
	if len(def.Values) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(def.Values))
	for _, value := range def.Values {
		keys = append(keys, value.Key)
	}
	return strings.Join(keys, ", ")
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func VarName(addon, key string) string {
	name := "BEDROCK_" + addon + "_" + key
	return strings.NewReplacer(".", "_", "-", "_").Replace(strings.ToUpper(name))
}

func valuesOf(addon v1alpha1.Addon) (map[string]string, error) {
	if addon.Spec.Values == nil || len(addon.Spec.Values.Raw) == 0 {
		return map[string]string{}, nil
	}
	var tree map[string]any
	if err := json.Unmarshal(addon.Spec.Values.Raw, &tree); err != nil {
		return nil, fmt.Errorf("spec.values must be an object: %w", err)
	}
	flat := map[string]string{}
	flatten("", tree, flat)
	return flat, nil
}

func flatten(prefix string, node map[string]any, into map[string]string) {
	for key, value := range node {
		path := prefix + key
		if child, ok := value.(map[string]any); ok {
			flatten(path+".", child, into)
			continue
		}
		if text, ok := value.(string); ok {
			into[path] = text
			continue
		}
		raw, _ := json.Marshal(value)
		into[path] = string(raw)
	}
}
