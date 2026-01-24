package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func runGit(t *testing.T, repoPath string, args ...string) []byte {
	t.Helper()

	cmd := exec.Command("git", args...)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v failed: %v\n%s", args, err, output)
	}

	return output
}

func initRepo(t *testing.T) string {
	t.Helper()

	repoPath := t.TempDir()
	runGit(t, repoPath, "init")

	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("test"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	runGit(t, repoPath, "add", "README.md")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")

	return repoPath
}

// initRepoWithBranches creates a repo with an initial commit and additional branches.
// Each branch is created from the initial commit with its own unique commit.
func initRepoWithBranches(t *testing.T, branches ...string) string {
	t.Helper()

	repoPath := initRepo(t)

	for _, branch := range branches {
		runGit(t, repoPath, "checkout", "-b", branch)
		addCommits(t, repoPath, 1)
		runGit(t, repoPath, "checkout", "-")
	}

	return repoPath
}

// addCommits adds n commits to the current branch in the repo.
func addCommits(t *testing.T, repoPath string, n int) {
	t.Helper()

	for i := 0; i < n; i++ {
		filename := fmt.Sprintf("file_%d.txt", i)
		filePath := filepath.Join(repoPath, filename)
		if err := os.WriteFile(filePath, []byte(fmt.Sprintf("content %d", i)), 0644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		runGit(t, repoPath, "add", filename)
		runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
			"commit", "-m", fmt.Sprintf("commit %d", i))
	}
}

// createDirtyState creates uncommitted changes in the repo.
func createDirtyState(t *testing.T, repoPath string) {
	t.Helper()

	dirtyFile := filepath.Join(repoPath, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted changes"), 0644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
}

// assertFileContains checks that the file at path contains the expected string.
func assertFileContains(t *testing.T, path, expected string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}

	if !strings.Contains(string(content), expected) {
		t.Errorf("expected file %s to contain %q, got:\n%s", path, expected, content)
	}
}

// assertFileNotContains checks that the file at path does not contain the given string.
func assertFileNotContains(t *testing.T, path, notExpected string) {
	t.Helper()

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file %s: %v", path, err)
	}

	if strings.Contains(string(content), notExpected) {
		t.Errorf("expected file %s to NOT contain %q, but it does", path, notExpected)
	}
}
