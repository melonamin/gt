package main

import (
	"os"
	"path/filepath"
	"testing"
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
