package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/jgabor/ktx/internal/config"
	"github.com/jgabor/ktx/internal/db"
	"github.com/jgabor/ktx/internal/router"
)

func TestNoFileDefaults(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}

	image := router.Resolve(&db.ToolCall{Tool: "view_image"}, cfg.Routes, "default")
	if image.Profile != "passthrough" || image.Route != router.RouteMatched {
		t.Fatalf("view_image resolution = %+v", image)
	}
	profile := cfg.Profiles[image.Profile]
	if len(profile.Stages) != 0 || config.ResolveRecoverFooter(cfg, image.Profile) {
		t.Fatalf("passthrough profile = %#v", profile)
	}

	other := router.Resolve(&db.ToolCall{Tool: "read", Output: "unchanged"}, cfg.Routes, "default")
	if other.Profile != "default" || other.Route != router.RouteDefaultFallback {
		t.Fatalf("read resolution = %+v", other)
	}
}

func TestUserConfigIsNotAugmented(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	path := filepath.Join(root, "ktx", "ktx.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[pipeline.default]\nstages = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Profiles["passthrough"]; ok || len(cfg.Routes) != 0 {
		t.Fatalf("user config augmented: profiles=%v routes=%v", cfg.Profiles, cfg.Routes)
	}
}
