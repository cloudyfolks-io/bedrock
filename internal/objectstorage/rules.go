package objectstorage

import (
	"context"
	"slices"
	"strings"
)

const defaultMaxBuckets = 100

type Rules struct {
	ReservedNames    []string
	ReservedPrefixes []string
	CORSOrigins      []string
	MaxBuckets       int
	PublicHost       string
	PublicAliases    []string
}

func DefaultRules() Rules {
	return Rules{ReservedNames: []string{"admin"}, MaxBuckets: defaultMaxBuckets}
}

func (r *BucketReconciler) rules(ctx context.Context) (Rules, error) {
	return rulesFrom(ctx, r.Rules)
}

func rulesFrom(ctx context.Context, load func(context.Context) (Rules, error)) (Rules, error) {
	rules := DefaultRules()
	if load != nil {
		var err error
		if rules, err = load(ctx); err != nil {
			return Rules{}, err
		}
	}
	if rules.MaxBuckets <= 0 {
		rules.MaxBuckets = defaultMaxBuckets
	}
	return rules, nil
}

func mergeOrigins(lists ...[]string) []string {
	merged := []string{}
	for _, origin := range slices.Concat(lists...) {
		if origin != "" && !slices.Contains(merged, origin) {
			merged = append(merged, origin)
		}
	}
	return merged
}

func reservedName(name string, rules Rules) bool {
	if slices.Contains(rules.ReservedNames, name) {
		return true
	}
	return slices.ContainsFunc(rules.ReservedPrefixes, func(prefix string) bool { return strings.HasPrefix(name, prefix) })
}
