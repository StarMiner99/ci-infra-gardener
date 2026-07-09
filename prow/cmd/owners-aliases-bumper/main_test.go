package main

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/util/sets"
)

const sampleAliases = `# This is a top-of-file comment that should survive
aliases:
  # comment on team-a
  team-a:
    - alice
    - bob      # inline comment on bob
    - charlie
  team-b:
    - dave
    - eve
`

func TestWriteChanges(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "OWNERS_ALIASES")
	if err := os.WriteFile(path, []byte(sampleAliases), 0o644); err != nil {
		t.Fatal(err)
	}

	changes := map[string]change{
		"team-a": {
			add:    sets.New("frank"),
			remove: sets.New("bob"),
		},
		"team-b": {
			add:    sets.New("grace", "heidi"),
			remove: sets.New[string](),
		},
	}

	if err := writeChanges(path, changes); err != nil {
		t.Fatalf("writeChanges failed: %v", err)
	}

	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("=== BEFORE ===\n%s", sampleAliases)
	t.Logf("=== AFTER ===\n%s", string(out))
}
