package ci

import (
	"os"
	"strings"
	"testing"
)

func TestTrackedWorkflowUsesRepositoryVerification(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/ci.yml")
	for _, required := range []string{"push:", "pull_request:", "run: mage verify"} {
		if !strings.Contains(workflow, required) {
			t.Errorf("tracked workflow does not contain %q", required)
		}
	}
	for _, duplicatedGate := range []string{"mage replayFixture", "TestRetainedReplayFixtureGate"} {
		if strings.Contains(workflow, duplicatedGate) {
			t.Errorf("tracked workflow duplicates fixture gate logic with %q", duplicatedGate)
		}
	}

	magefile := readRepositoryFile(t, "magefile.go")
	verifyStart := strings.Index(magefile, "func Verify() error {")
	if verifyStart < 0 {
		t.Fatal("magefile has no Verify target")
	}
	verifyEnd := strings.Index(magefile[verifyStart:], "\n}")
	if verifyEnd < 0 {
		t.Fatal("cannot locate end of Verify target")
	}
	verify := magefile[verifyStart : verifyStart+verifyEnd]
	check := strings.Index(verify, "Check()")
	fixture := strings.Index(verify, "ReplayFixture()")
	if check < 0 || fixture < 0 || check > fixture {
		t.Errorf("Verify target must run Check before ReplayFixture:\n%s", verify)
	}
}

func readRepositoryFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile("../../" + path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}
