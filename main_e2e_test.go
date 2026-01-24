package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()

	projectRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}

	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "gt")

	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	return binaryPath
}

func TestGTRunCreatesWorktreeAndUpdatesExcludes(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "-x", "true", "feature-branch")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-branch")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree to exist: %v", err)
	}

	excludePath, err := gitExcludePath(repoPath)
	if err != nil {
		t.Fatalf("resolve git excludes path: %v", err)
	}

	content, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("read git excludes: %v", err)
	}

	entry := defaultWorktreeDir + "/"
	if !strings.Contains(string(content), entry) {
		t.Fatalf("expected %q in git excludes", entry)
	}
}

func TestGTCreateWorktreeFromSourceBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a source branch with extra commits
	runGit(t, repoPath, "checkout", "-b", "develop")
	addCommits(t, repoPath, 2)
	runGit(t, repoPath, "checkout", "master")

	// Create worktree from develop
	cmd := exec.Command(binaryPath, "-x", "true", "feature-from-develop", "develop")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-from-develop")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree to exist: %v", err)
	}

	// Verify the worktree has the commits from develop
	logOutput := runGit(t, worktreePath, "log", "--oneline")
	if !strings.Contains(string(logOutput), "commit 1") {
		t.Error("expected worktree to have commits from develop branch")
	}
}

func TestGTCreateWorktreeInvalidBranchName(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Try to create worktree with invalid branch name
	cmd := exec.Command(binaryPath, "-x", "true", "feature;rm -rf")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	// Should fail
	if err == nil {
		t.Fatal("expected command to fail for invalid branch name")
	}

	// Worktree should not exist
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature;rm -rf")
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should not have been created for invalid branch name")
	}

	// Output should mention invalid
	if !strings.Contains(string(output), "invalid") && !strings.Contains(string(output), "Invalid") {
		t.Logf("output: %s", output)
	}
}

func TestGTCreateWorktreeWithCustomDir(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Set up config with custom worktree dir
	customDir := filepath.Join(repoPath, "custom-worktrees")
	configDir := filepath.Join(t.TempDir(), "gt")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configContent := `{"worktree_dir": "custom-worktrees"}`
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binaryPath, "-x", "true", "custom-feature")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Dir(configDir))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(customDir, "custom-feature")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree in custom dir: %v", err)
	}
}

func TestGTMergeAndDeleteWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree with commits
	cmd := exec.Command(binaryPath, "-x", "true", "feature-to-merge")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add commits to the feature branch
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-to-merge")
	addCommits(t, worktreePath, 2)

	// Merge with ff-only
	cmd = exec.Command(binaryPath, "--merge", "feature-to-merge", "--ff-only")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("merge failed: %v\n%s", err, output)
	}

	// Worktree should be deleted
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should have been deleted after merge")
	}

	// Branch should be deleted
	branchOutput := runGit(t, repoPath, "branch", "--list", "feature-to-merge")
	if strings.TrimSpace(string(branchOutput)) != "" {
		t.Error("branch should have been deleted after merge")
	}
}

func TestGTMergeSquash(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree with commits
	cmd := exec.Command(binaryPath, "-x", "true", "feature-squash")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add multiple commits
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-squash")
	addCommits(t, worktreePath, 3)

	// Merge with squash
	cmd = exec.Command(binaryPath, "--merge", "feature-squash", "--squash")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("squash merge failed: %v\n%s", err, output)
	}

	// Worktree and branch should be deleted
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should have been deleted after squash merge")
	}

	// Check that the squash commit exists
	logOutput := runGit(t, repoPath, "log", "--oneline", "-1")
	if !strings.Contains(string(logOutput), "Squash merge") {
		t.Errorf("expected squash merge commit, got: %s", logOutput)
	}
}

func TestGTMergeFailsDirtyWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "dirty-feature")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add commits
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "dirty-feature")
	addCommits(t, worktreePath, 1)

	// Make the worktree dirty
	createDirtyState(t, worktreePath)

	// Try to merge - should fail
	cmd = exec.Command(binaryPath, "--merge", "dirty-feature")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected merge to fail for dirty worktree")
	}

	if !strings.Contains(string(output), "uncommitted") {
		t.Logf("output: %s", output)
	}

	// Worktree should still exist
	if _, err := os.Stat(worktreePath); err != nil {
		t.Error("worktree should still exist after failed merge")
	}
}

func TestGTMergeFailsCurrentWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "current-feature")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "current-feature")
	addCommits(t, worktreePath, 1)

	// Try to merge from within the worktree - should fail
	cmd = exec.Command(binaryPath, "--merge", "current-feature")
	cmd.Dir = worktreePath
	output, err = cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected merge to fail when run from the worktree being merged")
	}

	if !strings.Contains(string(output), "current") {
		t.Logf("output: %s", output)
	}
}

func TestGTDeleteWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "to-delete")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "to-delete")

	// Delete the worktree using git directly (since gt delete requires TUI)
	runGit(t, repoPath, "worktree", "remove", worktreePath, "--force")

	// Verify it's gone
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree directory should have been deleted")
	}
}

func TestGTHelpFlag(t *testing.T) {
	binaryPath := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"short flag", []string{"-h"}},
		{"long flag", []string{"--help"}},
		{"help word", []string{"help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gt %v failed: %v\n%s", tt.args, err, output)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, "Git Worktree Manager") {
				t.Errorf("expected help output to contain 'Git Worktree Manager', got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, "USAGE:") {
				t.Errorf("expected help output to contain 'USAGE:', got:\n%s", outputStr)
			}
		})
	}
}

func TestGTVersionFlag(t *testing.T) {
	binaryPath := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"short flag", []string{"-v"}},
		{"long flag", []string{"--version"}},
		{"version word", []string{"version"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gt %v failed: %v\n%s", tt.args, err, output)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, "gt version") {
				t.Errorf("expected version output, got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, version) {
				t.Errorf("expected version %s in output, got:\n%s", version, outputStr)
			}
		})
	}
}

func TestGTCompletionBash(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "bash")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion bash failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "_gt_completions") {
		t.Errorf("expected bash completion function, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "complete -F") {
		t.Errorf("expected complete command, got:\n%s", outputStr)
	}
}

func TestGTCompletionZsh(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "zsh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion zsh failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "#compdef gt") {
		t.Errorf("expected zsh compdef, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "_arguments") {
		t.Errorf("expected _arguments, got:\n%s", outputStr)
	}
}

func TestGTCompletionFish(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "fish")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion fish failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "Fish completion for gt") {
		t.Errorf("expected fish completion header, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "complete -c gt") {
		t.Errorf("expected complete command, got:\n%s", outputStr)
	}
}

func TestGTMergeRequiresBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "--merge")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected --merge without branch to fail")
	}

	if !strings.Contains(string(output), "requires") {
		t.Logf("output: %s", output)
	}
}

func TestGTMergeNonexistentBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "--merge", "nonexistent-branch")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected --merge with nonexistent branch to fail")
	}

	if !strings.Contains(string(output), "not found") {
		t.Logf("output: %s", output)
	}
}
