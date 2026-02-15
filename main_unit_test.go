package main

import (
	"os"
	"strings"
	"testing"
	"time"
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

func TestValidateBranchName(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid names
		{"simple name", "feature", false},
		{"with slash", "feature/add-login", false},
		{"with numbers", "feature-123", false},
		{"with dash", "fix-bug", false},
		{"nested slashes", "user/feature/sub", false},
		{"underscore", "feature_test", false},

		// Invalid: empty or whitespace
		{"empty", "", true},
		{"only spaces", "   ", true},

		// Invalid: starts with special chars
		{"starts with dash", "-feature", true},
		{"starts with dot", ".feature", true},

		// Invalid: ends with special chars
		{"ends with dot", "feature.", true},
		{"ends with lock", "feature.lock", true},

		// Invalid: contains ..
		{"contains double dot", "feature..test", true},

		// Invalid: contains space
		{"contains space", "feature test", true},

		// Invalid: special git chars
		{"contains tilde", "feature~1", true},
		{"contains caret", "feature^2", true},
		{"contains colon", "feature:test", true},
		{"contains question", "feature?", true},
		{"contains asterisk", "feature*", true},
		{"contains bracket", "feature[1]", true},
		{"contains backslash", "feature\\test", true},

		// Invalid: shell metacharacters
		{"contains semicolon", "feature;rm -rf", true},
		{"contains ampersand", "feature&test", true},
		{"contains pipe", "feature|test", true},
		{"contains dollar", "feature$var", true},
		{"contains backtick", "feature`cmd`", true},
		{"contains paren open", "feature(test)", true},
		{"contains paren close", "feature)", true},
		{"contains less than", "feature<test", true},
		{"contains greater than", "feature>test", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateBranchName(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateBranchName(%q) error = %v, wantErr %v", tt.input, err, tt.wantErr)
			}
		})
	}
}

func TestTruncateString(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"empty string", "", 10, ""},
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"truncated with ellipsis", "hello world", 5, "hello..."},
		{"zero maxLen", "hello", 0, "..."},
		{"unicode characters", "日本語テスト", 3, "日本語..."},
		{"unicode unchanged", "日本語", 5, "日本語"},
		{"mixed unicode ascii", "hello日本語", 7, "hello日本..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateString(tt.input, tt.maxLen)
			if got != tt.want {
				t.Errorf("truncateString(%q, %d) = %q, want %q", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestIsExpectedWorktreePath(t *testing.T) {
	tests := []struct {
		name         string
		branch       string
		worktreePath string
		want         bool
	}{
		{"matching simple branch", "add-orm", "/repo/.worktrees/add-orm", true},
		{"matching branch with slash", "feature/login", "/repo/.worktrees/feature-login", true},
		{"non-matching folder", "add-orm", "/repo/.worktrees/custom-name", false},
		{"empty branch", "", "/repo/.worktrees/whatever", true},
		{"empty path", "main", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isExpectedWorktreePath(tt.branch, tt.worktreePath)
			if got != tt.want {
				t.Errorf("isExpectedWorktreePath(%q, %q) = %v, want %v", tt.branch, tt.worktreePath, got, tt.want)
			}
		})
	}
}

func TestTruncateToWidth(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		maxWidth int
		want     string
	}{
		{"fits within width", "hello", 10, "hello"},
		{"truncates with ellipsis", "Fix crash (2h ago)", 12, "Fix crash..."},
		{"zero width", "hello", 0, ""},
		{"very small width", "hello world", 3, "..."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := truncateToWidth(tt.input, tt.maxWidth)
			if got != tt.want {
				t.Errorf("truncateToWidth(%q, %d) = %q, want %q", tt.input, tt.maxWidth, got, tt.want)
			}
		})
	}
}

func TestFormatRelativeTime(t *testing.T) {
	now := time.Now()

	tests := []struct {
		name string
		time time.Time
		want string
	}{
		{"zero time", time.Time{}, ""},
		{"just now", now.Add(-30 * time.Second), "just now"},
		{"1 minute ago", now.Add(-1 * time.Minute), "1 minute ago"},
		{"5 minutes ago", now.Add(-5 * time.Minute), "5 minutes ago"},
		{"1 hour ago", now.Add(-1 * time.Hour), "1 hour ago"},
		{"3 hours ago", now.Add(-3 * time.Hour), "3 hours ago"},
		{"1 day ago", now.Add(-24 * time.Hour), "1 day ago"},
		{"3 days ago", now.Add(-3 * 24 * time.Hour), "3 days ago"},
		{"old date formatted", now.Add(-30 * 24 * time.Hour), now.Add(-30 * 24 * time.Hour).Format("Jan 2, 2006")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatRelativeTime(tt.time)
			if got != tt.want {
				t.Errorf("formatRelativeTime() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFilterWorktrees(t *testing.T) {
	worktrees := []Worktree{
		{Branch: "main", Path: "/repo/main", LastCommit: CommitInfo{Message: "initial commit"}},
		{Branch: "feature-auth", Path: "/repo/.worktrees/feature-auth", LastCommit: CommitInfo{Message: "add login"}},
		{Branch: "fix-bug-123", Path: "/repo/.worktrees/fix-bug-123", LastCommit: CommitInfo{Message: "fix crash"}},
		{Branch: "develop", Path: "/repo/.worktrees/develop", LastCommit: CommitInfo{Message: "merged changes"}},
	}

	tests := []struct {
		name   string
		search string
		want   int
	}{
		{"empty search returns all", "", 4},
		{"match by branch", "feature-auth", 1},
		{"match by path", "worktrees", 3},
		{"match by commit message", "crash", 1},
		{"case insensitive branch", "MAIN", 1},
		{"case insensitive message", "LOGIN", 1},
		{"no matches", "nonexistent", 0},
		{"partial match", "auth", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := filterWorktrees(worktrees, tt.search)
			if len(got) != tt.want {
				t.Errorf("filterWorktrees(%q) returned %d worktrees, want %d", tt.search, len(got), tt.want)
			}
		})
	}
}

func TestGetShell(t *testing.T) {
	tests := []struct {
		name     string
		config   *Config
		envShell string
		want     string
		setEnv   bool
		clearEnv bool
	}{
		{"config shell preferred", &Config{Shell: "/bin/zsh"}, "/bin/bash", "/bin/zsh", true, false},
		{"env fallback when no config shell", &Config{}, "/bin/fish", "/bin/fish", true, false},
		{"nil config uses env", nil, "/bin/ksh", "/bin/ksh", true, false},
		{"default bash when no env", &Config{}, "", "/bin/bash", false, true},
		{"empty config shell uses env", &Config{Shell: ""}, "/usr/bin/zsh", "/usr/bin/zsh", true, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.clearEnv {
				t.Setenv("SHELL", "")
			} else if tt.setEnv {
				t.Setenv("SHELL", tt.envShell)
			}

			got := getShell(tt.config)
			if got != tt.want {
				t.Errorf("getShell() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name           string
		args           []string
		wantWorktree   string
		wantSource     string
		wantExecute    string
		wantCompletion string
		wantHelp       bool
		wantVersion    bool
		wantMerge      bool
		wantMergeStrat string
	}{
		{"no args", []string{"gt"}, "", "", "", "", false, false, false, ""},
		{"help short", []string{"gt", "-h"}, "", "", "", "", true, false, false, ""},
		{"help long", []string{"gt", "--help"}, "", "", "", "", true, false, false, ""},
		{"help word", []string{"gt", "help"}, "", "", "", "", true, false, false, ""},
		{"version short", []string{"gt", "-v"}, "", "", "", "", false, true, false, ""},
		{"version long", []string{"gt", "--version"}, "", "", "", "", false, true, false, ""},
		{"version word", []string{"gt", "version"}, "", "", "", "", false, true, false, ""},
		{"worktree name only", []string{"gt", "feature-x"}, "feature-x", "", "", "", false, false, false, ""},
		{"worktree with source", []string{"gt", "feature-x", "main"}, "feature-x", "main", "", "", false, false, false, ""},
		{"execute short", []string{"gt", "feature-x", "-x", "npm install"}, "feature-x", "", "npm install", "", false, false, false, ""},
		{"execute long", []string{"gt", "feature-x", "--execute", "npm install"}, "feature-x", "", "npm install", "", false, false, false, ""},
		{"execute equals short", []string{"gt", "feature-x", "-x=code ."}, "feature-x", "", "code .", "", false, false, false, ""},
		{"execute equals long", []string{"gt", "feature-x", "--execute=code ."}, "feature-x", "", "code .", "", false, false, false, ""},
		{"completion bash", []string{"gt", "completion", "bash"}, "", "", "", "bash", false, false, false, ""},
		{"completion zsh", []string{"gt", "completion", "zsh"}, "", "", "", "zsh", false, false, false, ""},
		{"completion fish", []string{"gt", "completion", "fish"}, "", "", "", "fish", false, false, false, ""},
		{"completion default", []string{"gt", "completion"}, "", "", "", "bash", false, false, false, ""},
		{"merge mode", []string{"gt", "--merge", "feature-x"}, "feature-x", "", "", "", false, false, true, "ff-only"},
		{"merge with squash", []string{"gt", "--merge", "feature-x", "--squash"}, "feature-x", "", "", "", false, false, true, "squash"},
		{"merge with ff-only", []string{"gt", "--merge", "feature-x", "--ff-only"}, "feature-x", "", "", "", false, false, true, "ff-only"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore os.Args
			oldArgs := os.Args
			defer func() { os.Args = oldArgs }()
			os.Args = tt.args

			worktree, source, execute, completion, help, version, merge, mergeStrat := parseArgs()

			if worktree != tt.wantWorktree {
				t.Errorf("worktree = %q, want %q", worktree, tt.wantWorktree)
			}
			if source != tt.wantSource {
				t.Errorf("source = %q, want %q", source, tt.wantSource)
			}
			if execute != tt.wantExecute {
				t.Errorf("execute = %q, want %q", execute, tt.wantExecute)
			}
			if completion != tt.wantCompletion {
				t.Errorf("completion = %q, want %q", completion, tt.wantCompletion)
			}
			if help != tt.wantHelp {
				t.Errorf("help = %v, want %v", help, tt.wantHelp)
			}
			if version != tt.wantVersion {
				t.Errorf("version = %v, want %v", version, tt.wantVersion)
			}
			if merge != tt.wantMerge {
				t.Errorf("merge = %v, want %v", merge, tt.wantMerge)
			}
			if mergeStrat != tt.wantMergeStrat {
				t.Errorf("mergeStrat = %q, want %q", mergeStrat, tt.wantMergeStrat)
			}
		})
	}
}
