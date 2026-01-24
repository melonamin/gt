package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestEnsureNoTrackedFilesFailsForWorktreeDir(t *testing.T) {
	repoPath := initRepo(t)

	worktreeDir := filepath.Join(repoPath, defaultWorktreeDir)
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}

	trackedFile := filepath.Join(worktreeDir, "tracked.txt")
	if err := os.WriteFile(trackedFile, []byte("tracked"), 0644); err != nil {
		t.Fatalf("write tracked file: %v", err)
	}

	runGit(t, repoPath, "add", filepath.ToSlash(filepath.Join(defaultWorktreeDir, "tracked.txt")))

	if err := ensureNoTrackedFiles(repoPath, defaultWorktreeDir); err == nil {
		t.Fatalf("expected tracked files error")
	}
}

func TestEnsureNoTrackedFilesPassesForEmptyDir(t *testing.T) {
	repoPath := initRepo(t)

	if err := ensureNoTrackedFiles(repoPath, defaultWorktreeDir); err != nil {
		t.Fatalf("expected no error for non-existent dir: %v", err)
	}
}

func TestGetAheadBehindWithContext(t *testing.T) {
	repoPath := initRepo(t)
	ctx := context.Background()

	// Create a branch with commits ahead of main
	runGit(t, repoPath, "checkout", "-b", "feature")
	addCommits(t, repoPath, 2)
	runGit(t, repoPath, "checkout", "-")

	tests := []struct {
		name          string
		branch        string
		defaultBranch string
		wantAhead     int
		wantBehind    int
	}{
		{"same as default", "master", "master", 0, 0},
		{"empty branch", "", "master", 0, 0},
		{"branch ahead", "feature", "master", 2, 0},
		{"invalid branch", "nonexistent", "master", 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ahead, behind := getAheadBehindWithContext(ctx, repoPath, tt.branch, tt.defaultBranch)
			if ahead != tt.wantAhead {
				t.Errorf("ahead = %d, want %d", ahead, tt.wantAhead)
			}
			if behind != tt.wantBehind {
				t.Errorf("behind = %d, want %d", behind, tt.wantBehind)
			}
		})
	}
}

func TestGetAheadBehindDiverged(t *testing.T) {
	repoPath := initRepo(t)
	ctx := context.Background()

	// Get the default branch name (might be 'main' or 'master' depending on git version)
	defaultBranch := strings.TrimSpace(string(runGit(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD")))

	// Create feature branch from initial commit and add unique commits
	runGit(t, repoPath, "checkout", "-b", "feature-diverged")

	// Add 2 commits to feature branch with unique files
	for i := 0; i < 2; i++ {
		filename := fmt.Sprintf("feature_file_%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		if err := os.WriteFile(filePath, []byte(fmt.Sprintf("feature content %d", i)), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGit(t, repoPath, "add", filename)
		runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
			"commit", "-m", fmt.Sprintf("feature commit %d", i))
	}

	// Go back to default branch and add commits there too (diverged)
	runGit(t, repoPath, "checkout", defaultBranch)

	// Add 1 commit to default branch with unique file
	masterFile := filepath.Join(repoPath, "master_file.txt")
	if err := os.WriteFile(masterFile, []byte("master content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoPath, "add", "master_file.txt")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "master commit")

	ahead, behind := getAheadBehindWithContext(ctx, repoPath, "feature-diverged", defaultBranch)

	if ahead != 2 {
		t.Errorf("ahead = %d, want 2", ahead)
	}
	if behind != 1 {
		t.Errorf("behind = %d, want 1", behind)
	}
}

func TestListWorktreesWithContext(t *testing.T) {
	repoPath := initRepo(t)
	ctx := context.Background()

	// Create a worktree
	worktreeDir := filepath.Join(repoPath, defaultWorktreeDir)
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		t.Fatalf("create worktree dir: %v", err)
	}

	worktreePath := filepath.Join(worktreeDir, "feature")
	runGit(t, repoPath, "worktree", "add", "-b", "feature", worktreePath)

	worktrees, defaultBranch, err := listWorktreesWithContext(ctx, repoPath)
	if err != nil {
		t.Fatalf("listWorktreesWithContext: %v", err)
	}

	if len(worktrees) != 2 {
		t.Errorf("got %d worktrees, want 2", len(worktrees))
	}

	// Check that we have both main and feature
	branches := make(map[string]bool)
	for _, wt := range worktrees {
		branches[wt.Branch] = true
	}

	if !branches["master"] && !branches["main"] {
		t.Error("expected master or main branch in worktrees")
	}
	if !branches["feature"] {
		t.Error("expected feature branch in worktrees")
	}

	// Default branch should be detected
	if defaultBranch != "master" && defaultBranch != "main" {
		t.Errorf("defaultBranch = %q, want master or main", defaultBranch)
	}
}

func TestRefExists(t *testing.T) {
	repoPath := initRepo(t)

	// Create a branch
	runGit(t, repoPath, "branch", "feature")

	tests := []struct {
		name string
		ref  string
		want bool
	}{
		{"existing branch", "feature", true},
		{"master branch", "master", true},
		{"HEAD ref", "HEAD", true},
		{"non-existing branch", "nonexistent", false},
		{"invalid ref", "refs/invalid/path", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refExists(repoPath, tt.ref)
			if got != tt.want {
				t.Errorf("refExists(%q) = %v, want %v", tt.ref, got, tt.want)
			}
		})
	}
}

func TestLocalBranchExists(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "branch", "local-feature")

	if !localBranchExists(repoPath, "local-feature") {
		t.Error("expected local-feature to exist")
	}

	if localBranchExists(repoPath, "nonexistent") {
		t.Error("expected nonexistent to not exist")
	}
}

func TestGetDefaultBranchWithContext(t *testing.T) {
	repoPath := initRepo(t)
	ctx := context.Background()

	// Without remote, should fall back to detecting main/master
	defaultBranch := getDefaultBranchWithContext(ctx, repoPath)

	// The init creates master by default
	if defaultBranch != "master" && defaultBranch != "main" {
		t.Errorf("defaultBranch = %q, want master or main", defaultBranch)
	}
}

func TestCanFastForward(t *testing.T) {
	repoPath := initRepo(t)

	// Create a branch that can be fast-forwarded
	runGit(t, repoPath, "checkout", "-b", "feature")
	addCommits(t, repoPath, 2)
	runGit(t, repoPath, "checkout", "master")

	canFF, err := canFastForward(repoPath, "feature", "master")
	if err != nil {
		t.Fatalf("canFastForward: %v", err)
	}
	if !canFF {
		t.Error("expected fast-forward to be possible")
	}
}

func TestCanFastForwardDiverged(t *testing.T) {
	repoPath := initRepo(t)

	// Get the default branch name
	defaultBranch := strings.TrimSpace(string(runGit(t, repoPath, "rev-parse", "--abbrev-ref", "HEAD")))

	// Create a branch with unique file
	runGit(t, repoPath, "checkout", "-b", "feature-ff-test")
	featureFile := filepath.Join(repoPath, "feature_ff_file.txt")
	if err := os.WriteFile(featureFile, []byte("feature"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoPath, "add", "feature_ff_file.txt")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "feature commit")

	// Add commits to default branch (diverge) with unique file
	runGit(t, repoPath, "checkout", defaultBranch)
	masterFile := filepath.Join(repoPath, "master_ff_file.txt")
	if err := os.WriteFile(masterFile, []byte("master"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, repoPath, "add", "master_ff_file.txt")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "master commit")

	canFF, err := canFastForward(repoPath, "feature-ff-test", defaultBranch)
	if err != nil {
		t.Fatalf("canFastForward: %v", err)
	}
	if canFF {
		t.Error("expected fast-forward to NOT be possible when diverged")
	}
}

func TestCanFastForwardSameCommit(t *testing.T) {
	repoPath := initRepo(t)

	// Create a branch at same commit
	runGit(t, repoPath, "branch", "feature")

	canFF, err := canFastForward(repoPath, "feature", "master")
	if err != nil {
		t.Fatalf("canFastForward: %v", err)
	}
	if !canFF {
		t.Error("expected fast-forward to be possible for same commit")
	}
}

func TestLoadConfigMissingFile(t *testing.T) {
	// Set XDG_CONFIG_HOME to a temp dir without config
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	// Should return empty config, not error
	if config == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestLoadConfigValidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, configDirName)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configData := `{"worktree_dir": "custom-worktrees", "shell": "/bin/zsh"}`
	configPath := filepath.Join(configDir, configFileName)
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if config.WorktreeDir != "custom-worktrees" {
		t.Errorf("WorktreeDir = %q, want custom-worktrees", config.WorktreeDir)
	}
	if config.Shell != "/bin/zsh" {
		t.Errorf("Shell = %q, want /bin/zsh", config.Shell)
	}
}

func TestLoadConfigInvalidJSON(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	configDir := filepath.Join(tmpDir, configDirName)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, configFileName)
	if err := os.WriteFile(configPath, []byte("invalid json {"), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	config, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig should not error on invalid JSON: %v", err)
	}

	// Should return empty config on invalid JSON
	if config == nil {
		t.Fatal("expected non-nil config")
	}
}

func TestSaveConfigCreatesDirectory(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	config := &Config{
		WorktreeDir: "my-worktrees",
		Shell:       "/bin/fish",
	}

	if err := saveConfig(config); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	// Verify file was created
	configPath := filepath.Join(tmpDir, configDirName, configFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}

	var loaded Config
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("unmarshal config: %v", err)
	}

	if loaded.WorktreeDir != config.WorktreeDir {
		t.Errorf("WorktreeDir = %q, want %q", loaded.WorktreeDir, config.WorktreeDir)
	}
}

func TestSaveAndLoadConfigRoundTrip(t *testing.T) {
	tmpDir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmpDir)

	original := &Config{
		WorktreeDir:          "../worktrees",
		Shell:                "/bin/zsh",
		PostCreate:           "npm install",
		DefaultMergeStrategy: "squash",
		GitTimeout:           10,
		GitSlowTimeout:       20,
	}

	if err := saveConfig(original); err != nil {
		t.Fatalf("saveConfig: %v", err)
	}

	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}

	if loaded.WorktreeDir != original.WorktreeDir {
		t.Errorf("WorktreeDir = %q, want %q", loaded.WorktreeDir, original.WorktreeDir)
	}
	if loaded.Shell != original.Shell {
		t.Errorf("Shell = %q, want %q", loaded.Shell, original.Shell)
	}
	if loaded.PostCreate != original.PostCreate {
		t.Errorf("PostCreate = %q, want %q", loaded.PostCreate, original.PostCreate)
	}
	if loaded.DefaultMergeStrategy != original.DefaultMergeStrategy {
		t.Errorf("DefaultMergeStrategy = %q, want %q", loaded.DefaultMergeStrategy, original.DefaultMergeStrategy)
	}
	if loaded.GitTimeout != original.GitTimeout {
		t.Errorf("GitTimeout = %d, want %d", loaded.GitTimeout, original.GitTimeout)
	}
	if loaded.GitSlowTimeout != original.GitSlowTimeout {
		t.Errorf("GitSlowTimeout = %d, want %d", loaded.GitSlowTimeout, original.GitSlowTimeout)
	}
}

func TestConfigGetGitTimeout(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   time.Duration
	}{
		{"nil config", nil, gitCmdTimeout},
		{"zero timeout", &Config{GitTimeout: 0}, gitCmdTimeout},
		{"custom timeout", &Config{GitTimeout: 15}, 15 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.getGitTimeout()
			if got != tt.want {
				t.Errorf("getGitTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestConfigGetGitSlowTimeout(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		want   time.Duration
	}{
		{"nil config", nil, gitCmdSlowTimeout},
		{"zero timeout", &Config{GitSlowTimeout: 0}, gitCmdSlowTimeout},
		{"custom timeout", &Config{GitSlowTimeout: 30}, 30 * time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.config.getGitSlowTimeout()
			if got != tt.want {
				t.Errorf("getGitSlowTimeout() = %v, want %v", got, tt.want)
			}
		})
	}
}
