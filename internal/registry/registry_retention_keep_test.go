package registry

import (
	"context"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func existingPolicy(state *harborState, project string) {
	state.mu.Lock()
	defer state.mu.Unlock()
	state.retentions[900] = map[string]any{"id": float64(900), "algorithm": "or", "rules": []any{map[string]any{"template": "latestPushedK"}}}
	state.projects[project].retentionID = 900
}

func adoptedRegistry(spec *v1alpha1.RegistryRetention) *v1alpha1.Registry {
	reg := registryFor("images", "acme-images")
	reg.Annotations = map[string]string{AnnotationAdopt: "true"}
	reg.Spec.Retention = spec
	return reg
}

func TestAdoptionKeepsTheRetentionPolicyWhenTheSpecHasNone(t *testing.T) {
	state := newHarborState(t)
	state.addProject("acme-images", 5)
	existingPolicy(state, "acme-images")
	r, c := registryWorld(t, state, adoptedRegistry(nil))
	reconcileRegistry(t, r, "images")
	reconcileRegistry(t, r, "images")
	if rules, _ := retentionOf(state, "acme-images")["rules"].([]any); len(rules) != 1 {
		t.Fatalf("policy %v", retentionOf(state, "acme-images"))
	}
	if fetchRegistry(t, c, "images").Status.RetentionApplied {
		t.Fatal("a kept policy is marked as applied")
	}
}

func TestAdoptionWithAnExplicitRetentionReplacesThePolicy(t *testing.T) {
	state := newHarborState(t)
	state.addProject("acme-images", 5)
	existingPolicy(state, "acme-images")
	spec := &v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(7))}
	r, c := registryWorld(t, state, adoptedRegistry(spec))
	reconcileRegistry(t, r, "images")
	rules, _ := retentionOf(state, "acme-images")["rules"].([]any)
	if len(rules) != 1 || rules[0].(map[string]any)["params"] == nil {
		t.Fatalf("policy %v", retentionOf(state, "acme-images"))
	}
	if !fetchRegistry(t, c, "images").Status.RetentionApplied {
		t.Fatal("an applied policy is not marked")
	}
}

func TestRemovedRetentionClearsOnlyWhatTheControllerApplied(t *testing.T) {
	state := newHarborState(t)
	reg := registryFor("images", "acme-images")
	reg.Spec.Retention = &v1alpha1.RegistryRetention{KeepLastN: ptr.To(int32(5))}
	r, c := registryWorld(t, state, reg)
	reconcileRegistry(t, r, "images")
	latest := fetchRegistry(t, c, "images")
	latest.Spec.Retention = nil
	if err := c.Update(context.Background(), latest); err != nil {
		t.Fatal(err)
	}
	reconcileRegistry(t, r, "images")
	if rules, _ := retentionOf(state, "acme-images")["rules"].([]any); len(rules) != 0 {
		t.Fatalf("rules %v", rules)
	}
	if fetchRegistry(t, c, "images").Status.RetentionApplied {
		t.Fatal("the mark stays after clearing")
	}
}
