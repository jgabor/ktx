package ci

import (
	"os"
	"strings"
	"testing"
)

func TestTrackedWorkflowUsesRepositoryVerification(t *testing.T) {
	workflow := readRepositoryFile(t, ".github/workflows/ci.yml")
	for _, required := range []string{
		"push:",
		"pull_request:",
		"go install golang.org/x/vuln/cmd/govulncheck@v1.6.0",
		"run: mage verify",
	} {
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
	publicVerify := strings.Index(magefile, `func Verify() error {
	return hermeticTarget("verify", verify)
}`)
	if publicVerify < 0 {
		t.Fatal("Verify target does not enforce the hermetic invocation boundary")
	}
	verifyStart := strings.Index(magefile, "func verify() error {")
	if verifyStart < 0 {
		t.Fatal("magefile has no nested Verify implementation")
	}
	verifyEnd := strings.Index(magefile[verifyStart:], "\n}")
	if verifyEnd < 0 {
		t.Fatal("cannot locate end of Verify target")
	}
	verify := magefile[verifyStart : verifyStart+verifyEnd]
	check := strings.Index(verify, "check()")
	vulnerability := strings.Index(verify, "Vuln()")
	fixture := strings.Index(verify, "replayFixture()")
	if check < 0 || vulnerability < 0 || fixture < 0 || check > vulnerability || vulnerability > fixture {
		t.Errorf("Verify target must run Check, Vuln, then ReplayFixture:\n%s", verify)
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
