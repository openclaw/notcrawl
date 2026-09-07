package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDesktopWorkspaceConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if _, err := WriteStarter(path); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil || len(cfg.Notion.Desktop.SpaceIDs) != 0 {
		t.Fatalf("starter must include all workspaces: %+v, %v", cfg.Notion.Desktop, err)
	}
	if err := os.WriteFile(path, []byte("[notion.desktop]\nspace_ids = ['workspace-a', 'workspace-b']\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = Load(path)
	if err != nil || len(cfg.Notion.Desktop.SpaceIDs) != 2 || cfg.Notion.Desktop.SpaceIDs[1] != "workspace-b" {
		t.Fatalf("workspace allowlist not loaded: %+v, %v", cfg.Notion.Desktop, err)
	}
	cfg.Notion.Desktop.SpaceIDs = []string{" "}
	if err := cfg.Resolve(); err == nil || !strings.Contains(err.Error(), "space_ids") {
		t.Fatalf("blank workspace ID accepted: %v", err)
	}
}

func TestDefaultUsesCurrentNotionAPIVersion(t *testing.T) {
	cfg := Default()
	if cfg.Notion.API.Version != "2026-03-11" {
		t.Fatalf("unexpected API version: %s", cfg.Notion.API.Version)
	}
}

func TestDefaultConfiguresExperimentalNotionMCPSource(t *testing.T) {
	cfg := Default()
	if cfg.Notion.MCP.Enabled {
		t.Fatal("Notion MCP should remain opt-in for sync --source all")
	}
	if cfg.Notion.MCP.ConnectorID != defaultMCPConnectorID || cfg.Notion.MCP.MaxPages != 100 {
		t.Fatalf("unexpected MCP defaults: %+v", cfg.Notion.MCP)
	}
	if err := cfg.Resolve(); err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(cfg.Notion.MCP.AuthPath) {
		t.Fatalf("auth path was not expanded: %q", cfg.Notion.MCP.AuthPath)
	}
}

func TestDefaultDesktopPathUsesPlatformCacheLocation(t *testing.T) {
	appData := filepath.Join(string(filepath.Separator), "appdata")
	if runtime.GOOS == "windows" {
		appData = `C:\appdata`
	}
	t.Setenv("APPDATA", appData)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(string(filepath.Separator), "xdg"))

	var want string
	switch runtime.GOOS {
	case "windows":
		want = filepath.Join(os.Getenv("APPDATA"), "Notion", "notion.db")
	case "darwin":
		want = "~/Library/Application Support/Notion/notion.db"
	default:
		want = filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "Notion", "notion.db")
	}
	if got := defaultDesktopPath(); got != want {
		t.Fatalf("default desktop path = %q, want %q", got, want)
	}
}

func TestDefaultDesktopPathFallsBackWhenPlatformEnvironmentIsMissing(t *testing.T) {
	t.Setenv("APPDATA", "")
	t.Setenv("XDG_CONFIG_HOME", "")

	var want string
	switch runtime.GOOS {
	case "windows":
		want = "~/AppData/Roaming/Notion/notion.db"
	case "darwin":
		want = "~/Library/Application Support/Notion/notion.db"
	default:
		want = "~/.config/Notion/notion.db"
	}
	if got := defaultDesktopPath(); got != want {
		t.Fatalf("fallback desktop path = %q, want %q", got, want)
	}
}
