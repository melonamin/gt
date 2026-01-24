package main

import (
	"path/filepath"
	"testing"
)

func TestGetConfigPathPrefersXdgConfigHome(t *testing.T) {
	xdgConfigHome := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("XDG_CONFIG_HOME", xdgConfigHome)

	configPath := getConfigPath()
	expected := filepath.Join(xdgConfigHome, configDirName, configFileName)
	if configPath != expected {
		t.Fatalf("expected config path %q, got %q", expected, configPath)
	}
}
