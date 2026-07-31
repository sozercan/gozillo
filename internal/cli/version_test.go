package cli

import "testing"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		linkedVersion string
		moduleVersion string
		want          string
	}{
		{
			name:          "linked release version",
			linkedVersion: "1.2.3",
			moduleVersion: "(devel)",
			want:          "1.2.3",
		},
		{
			name:          "linked version prefix",
			linkedVersion: "v1.2.3-rc.1",
			moduleVersion: "(devel)",
			want:          "1.2.3-rc.1",
		},
		{
			name:          "module version fallback",
			moduleVersion: "v2.0.0",
			want:          "2.0.0",
		},
		{
			name:          "development build",
			moduleVersion: "(devel)",
			want:          "(devel)",
		},
		{
			name: "missing build information",
			want: "unknown",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveVersion(test.linkedVersion, test.moduleVersion); got != test.want {
				t.Fatalf("resolveVersion(%q, %q) = %q, want %q", test.linkedVersion, test.moduleVersion, got, test.want)
			}
		})
	}
}
