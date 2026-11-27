package release

import (
	"fmt"
	"regexp"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

var Placeholder = regexp.MustCompile(`\$\{BEDROCK_[A-Z0-9_]+\}`)

func Substitute(bundle Bundle, vars map[string]string) (Bundle, error) {
	out := bundle
	out.Groups = make([]Group, 0, len(bundle.Groups))
	for _, group := range bundle.Groups {
		objects := make([]*unstructured.Unstructured, 0, len(group.Objects))
		for _, obj := range group.Objects {
			if !hasPlaceholder(obj.Object) {
				objects = append(objects, obj)
				continue
			}
			copied := obj.DeepCopy()
			if err := replacePlaceholders(copied.Object, vars); err != nil {
				return Bundle{}, fmt.Errorf("%s %s: %w", obj.GetKind(), obj.GetName(), err)
			}
			objects = append(objects, copied)
		}
		out.Groups = append(out.Groups, Group{Order: group.Order, Name: group.Name, Objects: objects})
	}
	return out, nil
}

func hasPlaceholder(node any) bool {
	switch value := node.(type) {
	case string:
		return Placeholder.MatchString(value)
	case map[string]any:
		for _, child := range value {
			if hasPlaceholder(child) {
				return true
			}
		}
	case []any:
		for _, child := range value {
			if hasPlaceholder(child) {
				return true
			}
		}
	}
	return false
}

func replacePlaceholders(node any, vars map[string]string) error {
	switch value := node.(type) {
	case map[string]any:
		for key, child := range value {
			replaced, err := replaceChild(child, vars)
			if err != nil {
				return err
			}
			value[key] = replaced
		}
	case []any:
		for i, child := range value {
			replaced, err := replaceChild(child, vars)
			if err != nil {
				return err
			}
			value[i] = replaced
		}
	}
	return nil
}

func replaceChild(child any, vars map[string]string) (any, error) {
	switch value := child.(type) {
	case string:
		return replaceString(value, vars)
	case []any:
		return replaceList(value, vars)
	}
	return child, replacePlaceholders(child, vars)
}

func replaceList(items []any, vars map[string]string) ([]any, error) {
	kept := make([]any, 0, len(items))
	for _, item := range items {
		text, isText := item.(string)
		if isText && Placeholder.MatchString(text) {
			replaced, err := replaceString(text, vars)
			if err != nil {
				return nil, err
			}
			if replaced == "" {
				continue
			}
			kept = append(kept, replaced)
			continue
		}
		replaced, err := replaceChild(item, vars)
		if err != nil {
			return nil, err
		}
		kept = append(kept, replaced)
	}
	return kept, nil
}

func replaceString(text string, vars map[string]string) (string, error) {
	var missing string
	out := Placeholder.ReplaceAllStringFunc(text, func(match string) string {
		name := match[2 : len(match)-1]
		value, ok := vars[name]
		if !ok {
			missing = name
			return match
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("unresolved placeholder %s", missing)
	}
	return out, nil
}
