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
