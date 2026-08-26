package db

import "testing"

func TestBlockerCommandLabelUsesClosedVocabulary(t *testing.T) {
	tests := []struct {
		argv []string
		want string
	}{
		{[]string{"ktx", "hook", "--payload", "secret"}, "hook"},
		{[]string{"ktx", "--format", "json", "get", "secret-id"}, "get"},
		{[]string{"ktx", "db", "report", "/secret/path"}, "db report"},
		{[]string{"ktx", "db", "--path", "/secret/path", "migrate"}, "db migrate"},
		{[]string{"ktx", "telemetry", "unsupported"}, "unknown"},
		{[]string{"ktx", "--unknown", "hook"}, "unknown"},
		{[]string{"ktx", "--format"}, "unknown"},
		{[]string{"ktx"}, "unknown"},
	}
	for _, test := range tests {
		if got := BlockerCommandLabel(test.argv); got != test.want {
			t.Errorf("BlockerCommandLabel(%q) = %q, want %q", test.argv, got, test.want)
		}
	}
}
