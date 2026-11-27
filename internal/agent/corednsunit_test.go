package agent

import "testing"

func TestWithCoreDNSDisabled(t *testing.T) {
	cases := []struct {
		name    string
		unit    string
		want    string
		changed bool
	}{
		{"equals form", "ExecStart=/k0s controller --disable-components=a,helm --enable-worker\n", "ExecStart=/k0s controller --disable-components=a,helm,coredns --enable-worker\n", true},
		{"space form", "ExecStart=/k0s controller --disable-components a,helm\n", "ExecStart=/k0s controller --disable-components a,helm,coredns\n", true},
		{"line continuation", "ExecStart=/k0s controller --disable-components=a \\\n  --enable-worker\n", "ExecStart=/k0s controller --disable-components=a,coredns \\\n  --enable-worker\n", true},
		{"already disabled", "ExecStart=/k0s controller --disable-components=a,coredns,helm\n", "ExecStart=/k0s controller --disable-components=a,coredns,helm\n", false},
		{"similar name", "ExecStart=/k0s controller --disable-components=my-coredns\n", "ExecStart=/k0s controller --disable-components=my-coredns,coredns\n", true},
		{"no flag", "ExecStart=/k0s controller --enable-worker\n", "ExecStart=/k0s controller --enable-worker\n", false},
	}
	for _, tc := range cases {
		got, changed := withCoreDNSDisabled(tc.unit)
		if got != tc.want || changed != tc.changed {
			t.Errorf("%s: got %q %v, want %q %v", tc.name, got, changed, tc.want, tc.changed)
		}
	}
}
