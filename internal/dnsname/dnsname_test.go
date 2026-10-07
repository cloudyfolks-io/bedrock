package dnsname

import (
	"strings"
	"testing"
)

func TestValid(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"empty", "", false},
		{"one label", "localhost", false},
		{"ip address", "192.168.5.250", false},
		{"ipv6 address", "::1", false},
		{"upper case", "UPPER.example.com", false},
		{"leading dash", "-a.example.com", false},
		{"trailing dash", "a-.example.com", false},
		{"empty label", "a..example.com", false},
		{"trailing dot", "example.com.", false},
		{"underscore", "a_b.example.com", false},
		{"long label", strings.Repeat("a", 64) + ".example.com", false},
		{"long name", strings.Repeat("a.", 127) + "aa", false},
		{"valid", "cloud.example.com", true},
		{"reserved tld", "e2e.bedrock.test", true},
		{"short", "a-b.c", true},
		{"longest label", strings.Repeat("a", 63) + ".example.com", true},
		{"longest name", strings.Repeat("a.", 125) + "aaa", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Valid(tc.in); got != tc.want {
				t.Fatalf("Valid(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
