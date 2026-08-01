package cli

import "testing"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		linkedVersion string
		moduleVersion string
		revision      string
		modified      bool
		want          string
	}{
		{
			name:          "linked release version",
			linkedVersion: "1.2.3",
			moduleVersion: "(devel)",
			revision:      "0123456789abcdef",
			modified:      true,
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
			revision:      "0123456789abcdef",
			want:          "2.0.0",
		},
		{
			name:          "development revision",
			moduleVersion: "(devel)",
			revision:      "0123456789abcdef",
			want:          "dev-0123456789ab",
		},
		{
			name:          "modified development revision",
			moduleVersion: "(devel)",
			revision:      "0123456789abcdef",
			modified:      true,
			want:          "dev-0123456789ab-dirty",
		},
		{
			name:     "short revision",
			revision: "abc123",
			want:     "dev-abc123",
		},
		{
			name: "missing build information",
			want: "dev",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveVersion(test.linkedVersion, test.moduleVersion, test.revision, test.modified); got != test.want {
				t.Fatalf(
					"resolveVersion(%q, %q, %q, %t) = %q, want %q",
					test.linkedVersion,
					test.moduleVersion,
					test.revision,
					test.modified,
					got,
					test.want,
				)
			}
		})
	}
}
