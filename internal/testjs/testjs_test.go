package testjs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeRuntime(t *testing.T, dir, name, report string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	script := "#!/bin/sh\nprintf 'setup noise\\n%s\\n' '" + report + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestResolveUsesProbedExecutable(t *testing.T) {
	bin := t.TempDir()
	fakeRuntime(t, bin, "node", `["/opt/node/bin/node","24.1.0",null]`)
	fakeRuntime(t, bin, "bun", `["/opt/bun/bin/bun","24.3.0","1.4.3"]`)
	env := []string{"PATH=" + bin}
	for name, want := range map[string]string{"node": "/opt/node/bin/node", "bun": "/opt/bun/bin/bun"} {
		if got := resolve(env, name); got.err != nil || got.path != want || got.explicit {
			t.Errorf("%s resolution = %+v, want %s", name, got, want)
		}
	}
}

func TestResolveHonorsExplicitOverride(t *testing.T) {
	explicit := fakeRuntime(t, t.TempDir(), "custom-node", `["/opt/explicit/node","22.18.0",null]`)
	got := resolve([]string{"PATH=", "KTX_TEST_NODE=" + explicit}, "node")
	if got.err != nil || got.path != "/opt/explicit/node" || !got.explicit {
		t.Fatalf("explicit resolution = %+v", got)
	}
	got = resolve([]string{"KTX_TEST_NODE=relative/node"}, "node")
	if got.err == nil || !got.explicit || !strings.Contains(got.err.Error(), "absolute") {
		t.Fatalf("relative override = %+v", got)
	}
}

func TestResolveRejectsWrongOrOldRuntime(t *testing.T) {
	bin := t.TempDir()
	fakeRuntime(t, bin, "node", `["/opt/node/bin/node","23.5.0",null]`)
	fakeRuntime(t, bin, "bun", `["/opt/node/bin/node","24.1.0",null]`)
	env := []string{"PATH=" + bin}
	if got := resolve(env, "node"); got.err == nil || !strings.Contains(got.err.Error(), "23.5.0") {
		t.Errorf("old node = %+v", got)
	}
	if got := resolve(env, "bun"); got.err == nil || !strings.Contains(got.err.Error(), "not Bun") {
		t.Errorf("node posing as bun = %+v", got)
	}
	if got := resolve([]string{"PATH=" + t.TempDir()}, "node"); got.err == nil || got.explicit || !strings.Contains(got.err.Error(), "KTX_TEST_NODE") {
		t.Errorf("missing node = %+v", got)
	}
}

func TestSupportedNode(t *testing.T) {
	for version, want := range map[string]bool{
		"22.17.1": false, "22.18.0": true, "23.5.0": false, "23.6.0": true, "24.0.0": true, "25.1.0": true, "20.19.0": false, "x": false,
	} {
		if got := supportedNode(version); got != want {
			t.Errorf("supportedNode(%q) = %t, want %t", version, got, want)
		}
	}
}
