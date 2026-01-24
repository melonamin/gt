package main

import (
	"os"
	"path/filepath"
	"strings"
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

func TestGetConfigPathFallsBackToUserConfigDir(t *testing.T) {
	// Clear XDG_CONFIG_HOME to test fallback
	t.Setenv("XDG_CONFIG_HOME", "")

	configPath := getConfigPath()

	// Should contain the config dir name and file name
	if !strings.Contains(configPath, configDirName) {
		t.Errorf("config path %q should contain %q", configPath, configDirName)
	}
	if !strings.HasSuffix(configPath, configFileName) {
		t.Errorf("config path %q should end with %q", configPath, configFileName)
	}
}

func TestConfigDefaultValues(t *testing.T) {
	config := &Config{}

	// Empty config should use defaults via getter methods
	if got := config.getGitTimeout(); got != gitCmdTimeout {
		t.Errorf("getGitTimeout() = %v, want %v", got, gitCmdTimeout)
	}
	if got := config.getGitSlowTimeout(); got != gitCmdSlowTimeout {
		t.Errorf("getGitSlowTimeout() = %v, want %v", got, gitCmdSlowTimeout)
	}

	// Empty fields should be empty
	if config.WorktreeDir != "" {
		t.Errorf("WorktreeDir should be empty, got %q", config.WorktreeDir)
	}
	if config.Shell != "" {
		t.Errorf("Shell should be empty, got %q", config.Shell)
	}
	if config.PostCreate != "" {
		t.Errorf("PostCreate should be empty, got %q", config.PostCreate)
	}
	if config.DefaultMergeStrategy != "" {
		t.Errorf("DefaultMergeStrategy should be empty, got %q", config.DefaultMergeStrategy)
	}
}

func TestLoadConfigWithAllFields(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, configDirName)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configData := `{
		"worktree_dir": "../my-worktrees",
		"shell": "/bin/fish",
		"post_create": "npm ci && npm run build",
		"default_merge_strategy": "squash",
		"git_timeout": 15,
		"git_slow_timeout": 30
	}`
	configPath := filepath.Join(configDir, configFileName)
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if config.WorktreeDir != "../my-worktrees" {
		t.Errorf("WorktreeDir = %q, want ../my-worktrees", config.WorktreeDir)
	}
	if config.Shell != "/bin/fish" {
		t.Errorf("Shell = %q, want /bin/fish", config.Shell)
	}
	if config.PostCreate != "npm ci && npm run build" {
		t.Errorf("PostCreate = %q, want 'npm ci && npm run build'", config.PostCreate)
	}
	if config.DefaultMergeStrategy != "squash" {
		t.Errorf("DefaultMergeStrategy = %q, want squash", config.DefaultMergeStrategy)
	}
	if config.GitTimeout != 15 {
		t.Errorf("GitTimeout = %d, want 15", config.GitTimeout)
	}
	if config.GitSlowTimeout != 30 {
		t.Errorf("GitSlowTimeout = %d, want 30", config.GitSlowTimeout)
	}
}

func TestLoadConfigIgnoresExtraFields(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, configDirName)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	// Config with unknown fields should still load known fields
	configData := `{
		"worktree_dir": "known-dir",
		"unknown_field": "should be ignored",
		"another_unknown": 42
	}`
	configPath := filepath.Join(configDir, configFileName)
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if config.WorktreeDir != "known-dir" {
		t.Errorf("WorktreeDir = %q, want known-dir", config.WorktreeDir)
	}
}

func TestSaveConfigFormatsNicely(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	config := &Config{
		WorktreeDir: "test-dir",
	}

	if err := saveConfig(config); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	configPath := filepath.Join(tmpDir, configDirName, configFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	content := string(data)

	// Should be indented (pretty printed)
	if !strings.Contains(content, "  ") {
		t.Errorf("expected indented JSON, got:\n%s", content)
	}
}
