package policy

import (
	"reflect"
	"testing"

	"github.com/cloudyfolks-io/bedrock/api/v1alpha1"
)

func group(name string, members []string, requireSecondFactor bool) v1alpha1.Group {
	g := v1alpha1.Group{Spec: v1alpha1.GroupSpec{Members: members, RequireSecondFactor: requireSecondFactor}}
	g.Name = name
	return g
}

func TestEffectiveGroups(t *testing.T) {
	user := v1alpha1.User{Spec: v1alpha1.UserSpec{Groups: []string{"editors", "editors"}}}
	user.Name = "alice"
	groups := []v1alpha1.Group{
		group("editors", nil, false),
		group("viewers", []string{"alice"}, false),
		group("billing", []string{"bob"}, false),
	}
	got := EffectiveGroups(user, groups)
	want := []string{"editors", "viewers"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("EffectiveGroups = %v, want %v", got, want)
	}
}

func TestSecondFactorRequired(t *testing.T) {
	user := v1alpha1.User{Spec: v1alpha1.UserSpec{Groups: []string{"staff"}}}
	user.Name = "alice"
	requiringGroups := []v1alpha1.Group{group("staff", nil, true)}
	plainGroups := []v1alpha1.Group{group("staff", nil, false)}
	requiringClient := v1alpha1.OAuthClient{Spec: v1alpha1.OAuthClientSpec{RequireSecondFactor: true}}
	plainClient := v1alpha1.OAuthClient{}
	cases := []struct {
		name     string
		settings Settings
		groups   []v1alpha1.Group
		client   v1alpha1.OAuthClient
		want     bool
	}{
		{"setting forces it", Settings{RequireSecondFactor: true}, plainGroups, plainClient, true},
		{"group forces it", Settings{}, requiringGroups, plainClient, true},
		{"client forces it", Settings{}, plainGroups, requiringClient, true},
		{"none of the three", Settings{}, plainGroups, plainClient, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SecondFactorRequired(c.settings, user, c.groups, c.client); got != c.want {
				t.Fatalf("SecondFactorRequired() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestSecondFactorSatisfied(t *testing.T) {
	cases := map[string]struct {
		amr  []string
		want bool
	}{
		"otp satisfies":      {[]string{"pwd", "otp"}, true},
		"mfa satisfies":      {[]string{"mfa"}, true},
		"hwk satisfies":      {[]string{"fed", "hwk"}, true},
		"pwd alone does not": {[]string{"pwd"}, false},
		"fed alone does not": {[]string{"fed"}, false},
		"empty does not":     {nil, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := SecondFactorSatisfied(c.amr); got != c.want {
				t.Fatalf("SecondFactorSatisfied(%v) = %v, want %v", c.amr, got, c.want)
			}
		})
	}
}

func TestMapGroups(t *testing.T) {
	mapping := []v1alpha1.GroupMapping{
		{External: "CN=Ops", Group: "operators"},
		{External: "CN=Dev", Group: "developers"},
		{External: "CN=QA", Group: "developers"},
	}
	got := MapGroups(mapping, []string{"CN=Dev", "CN=Unknown", "CN=QA", "CN=Ops"})
	want := []string{"developers", "operators"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MapGroups = %v, want %v", got, want)
	}
	if got := MapGroups(mapping, nil); len(got) != 0 {
		t.Fatalf("MapGroups(nil) = %v, want empty", got)
	}
}
