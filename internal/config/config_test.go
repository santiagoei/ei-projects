package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestWatchDirsFromShell(t *testing.T) {
	shell := filepath.Join(t.TempDir(), "shell.json")
	writeFile(t, shell, `{"bar":{"layout":{
	  "left":[{"id":"omarchy.keyboard-layout"},{"id":"ei.git-status","watchDirs":" /a/one, ~/two ,, "}],
	  "center":[{"id":"omarchy.clock"}]}}}`)
	t.Setenv("OMARCHY_SHELL_JSON", shell)

	dirs, ok, err := watchDirsFromShell()
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if len(dirs) != 2 || dirs[0] != "/a/one" || dirs[1] != "~/two" {
		t.Fatalf("unexpected dirs: %#v", dirs)
	}
}

func TestLoadResolvesAndAdoptsVault(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "proj")
	vault := filepath.Join(root, "obsidian")
	for _, d := range []string{proj, vault} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	shell := filepath.Join(root, "shell.json")
	// t.TempDir lives under /tmp; resolve() expands ~ against HOME, so point
	// HOME into the sandbox too and use a ~/ path for the vault.
	t.Setenv("HOME", root)
	body, _ := json.Marshal(map[string]any{
		"bar": map[string]any{"layout": map[string]any{
			"left": []any{map[string]any{"id": "ei.git-status", "watchDirs": proj + ", ~/obsidian"}}}}})
	writeFile(t, shell, string(body))
	t.Setenv("OMARCHY_SHELL_JSON", shell)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.WatchDirs) != 2 {
		t.Fatalf("watch dirs: %#v", cfg.WatchDirs)
	}
	if cfg.VaultDir != vault {
		t.Fatalf("vault not adopted: %q (want %q)", cfg.VaultDir, vault)
	}
	if cfg.MaxDepth != DefaultMaxDepth {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}

func TestLoadOverrideFile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("OMARCHY_SHELL_JSON", filepath.Join(root, "missing.json"))
	dir := filepath.Join(root, "only")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, ".config", "ei-projects", "config.json"),
		`{"watchDirs":["/only"],"vault":"","maxDepth":2,"sessionLimit":3}`)

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MaxDepth != 2 || cfg.SessionLimit != 3 || len(cfg.WatchDirs) != 1 {
		t.Fatalf("override not honored: %+v", cfg)
	}
}

func TestLoadNoSourcesIsError(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("OMARCHY_SHELL_JSON", filepath.Join(root, "missing.json"))
	if _, err := Load(); err == nil {
		t.Fatal("expected error with no watch source at all")
	}
}
