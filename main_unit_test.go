package main

import (
	"os"
	"strings"
	"testing"
)

func TestEnsureGitExcludeEntryAddsEntry(t *testing.T) {
	repoPath := initRepo(t)

	if err := ensureGitExcludeEntry(repoPath, defaultWorktreeDir); err != nil {
		t.Fatalf("ensure git exclude entry: %v", err)
	}

	if err := ensureGitExcludeEntry(repoPath, defaultWorktreeDir); err != nil {
		t.Fatalf("ensure git exclude entry again: %v", err)
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
	if strings.Count(string(content), entry) != 1 {
		t.Fatalf("expected single %q entry in git excludes", entry)
	}
}
