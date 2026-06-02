package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func runGit(t *testing.T, repoPath string, args ...string) []byte {
	t.Helper()

	gitArgs := append([]string{
		"-c", "commit.gpgsign=false",
		"-c", "tag.gpgsign=false",
	}, args...)
	cmd := exec.Command("git", gitArgs...)
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
	runGit(t, repoPath, "init", "--initial-branch=master")

	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("test"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}

	runGit(t, repoPath, "add", "README.md")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")

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
