package benchregistry

import "testing"

func TestActiveRegistryLookupAndSkillsBenchExclusion(t *testing.T) {
	r := Active()
	if entry, err := r.Lookup(Task, "crossfile-nav"); err != nil || entry.Path != "tools/bench/tasks/crossfile-nav.toml" {
		t.Fatalf("lookup = %#v, %v", entry, err)
	}
	if _, err := r.Lookup(Spec, "skillsbench"); err == nil {
		t.Fatal("SkillsBench must not be active")
	}
}

func TestRegistryRejectsUnknownDuplicateAndPathLikeIDs(t *testing.T) {
	r := Active()
	for _, id := range []string{"missing", "../crossfile-nav", "tasks/crossfile-nav", "crossfile-nav-v1"} {
		if _, err := r.Lookup(Task, id); err == nil {
			t.Fatalf("Lookup(%q) succeeded", id)
		}
	}
	if _, err := New([]Entry{{Task, "same", "toml", "a"}, {Task, "same", "toml", "b"}}); err == nil {
		t.Fatal("duplicate ID succeeded")
	}
	if _, err := New([]Entry{{Task, "task-v2", "toml", "task.toml"}}); err == nil {
		t.Fatal("versioned ID succeeded")
	}
}
