package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

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

func TestParseGitHubRemoteURL(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  githubRepo
		ok    bool
	}{
		{"https", "https://github.com/melonamin/gt.git", githubRepo{Owner: "melonamin", Name: "gt"}, true},
		{"ssh url", "ssh://git@github.com/melonamin/gt.git", githubRepo{Owner: "melonamin", Name: "gt"}, true},
		{"scp ssh", "git@github.com:melonamin/gt.git", githubRepo{Owner: "melonamin", Name: "gt"}, true},
		{"without git suffix", "https://github.com/melonamin/gt", githubRepo{Owner: "melonamin", Name: "gt"}, true},
		{"enterprise out of scope", "https://github.example.com/melonamin/gt.git", githubRepo{}, false},
		{"not github", "git@gitlab.com:melonamin/gt.git", githubRepo{}, false},
		{"invalid", "not-a-url", githubRepo{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseGitHubRemoteURL(tt.input)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("repo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestChooseBaseGitHubRepo(t *testing.T) {
	tests := []struct {
		name    string
		remotes []gitRemote
		want    githubRepo
		ok      bool
	}{
		{
			name: "upstream preferred over origin",
			remotes: []gitRemote{
				{Name: "origin", URL: "https://github.com/fishy/gt.git"},
				{Name: "upstream", URL: "https://github.com/melonamin/gt.git"},
			},
			want: githubRepo{Owner: "melonamin", Name: "gt"},
			ok:   true,
		},
		{
			name: "origin preferred over other github remote",
			remotes: []gitRemote{
				{Name: "fork", URL: "https://github.com/fishy/gt.git"},
				{Name: "origin", URL: "https://github.com/melonamin/gt.git"},
			},
			want: githubRepo{Owner: "melonamin", Name: "gt"},
			ok:   true,
		},
		{
			name: "first github remote fallback",
			remotes: []gitRemote{
				{Name: "mirror", URL: "https://gitlab.com/melonamin/gt.git"},
				{Name: "fork", URL: "git@github.com:fishy/gt.git"},
			},
			want: githubRepo{Owner: "fishy", Name: "gt"},
			ok:   true,
		},
		{
			name: "no github remotes",
			remotes: []gitRemote{
				{Name: "origin", URL: "https://gitlab.com/melonamin/gt.git"},
			},
			want: githubRepo{},
			ok:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := chooseBaseGitHubRepo(tt.remotes)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Fatalf("repo = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGitHubRemoteOwners(t *testing.T) {
	got := githubRemoteOwners([]gitRemote{
		{Name: "origin", URL: "git@github.com:fishy/gt.git"},
		{Name: "upstream", URL: "https://github.com/melonamin/gt.git"},
		{Name: "other", URL: "https://gitlab.com/example/gt.git"},
	}, githubRepo{Owner: "melonamin", Name: "gt"})

	for _, owner := range []string{"fishy", "melonamin"} {
		if !got[owner] {
			t.Fatalf("expected owner %q in %#v", owner, got)
		}
	}
	if got["example"] {
		t.Fatalf("did not expect non-GitHub owner in %#v", got)
	}
}

func TestMapPullRequestStates(t *testing.T) {
	var response githubPullRequestGraphQLResponse
	response.Data.Repository = map[string]githubPullRequestConnection{
		"pr0": {Nodes: []githubPullRequestNode{pullRequestNode("OPEN", "aaa", "fishy")}},
		"pr1": {Nodes: []githubPullRequestNode{pullRequestNode("CLOSED", "bbb", "fishy")}},
		"pr2": {Nodes: []githubPullRequestNode{pullRequestNode("MERGED", "ccc", "melonamin")}},
		"pr3": {Nodes: nil},
	}

	got := mapPullRequestStates([]pullRequestLookup{
		{Branch: "feature", Head: "aaa", HeadOwners: map[string]bool{"fishy": true}},
		{Branch: "bugfix", Head: "bbb", HeadOwners: map[string]bool{"fishy": true}},
		{Branch: "done", Head: "ccc", HeadOwners: map[string]bool{"melonamin": true}},
		{Branch: "empty", Head: "ddd", HeadOwners: map[string]bool{"fishy": true}},
	}, response)
	want := map[string]pullRequestState{
		"feature": pullRequestStateOpen,
		"bugfix":  pullRequestStateClosed,
		"done":    pullRequestStateMerged,
	}

	if len(got) != len(want) {
		t.Fatalf("got %d states, want %d: %#v", len(got), len(want), got)
	}
	for branch, state := range want {
		if got[branch] != state {
			t.Fatalf("state for %s = %q, want %q", branch, got[branch], state)
		}
	}
	if _, ok := got["empty"]; ok {
		t.Fatalf("expected branch without PR nodes to be omitted")
	}
}

func TestUniquePullRequestLookupsUsesConfiguredUpstream(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://github.com/fishy/gt.git")
	runGit(t, repoPath, "remote", "add", "upstream", "https://github.com/melonamin/gt.git")
	runGit(t, repoPath, "config", "branch.local-fix.remote", "origin")
	runGit(t, repoPath, "config", "branch.local-fix.merge", "refs/heads/fix/123")

	lookups := uniquePullRequestLookups(
		context.Background(),
		repoPath,
		[]Worktree{{Branch: "local-fix", Head: "abc123"}},
		[]gitRemote{
			{Name: "origin", URL: "https://github.com/fishy/gt.git"},
			{Name: "upstream", URL: "https://github.com/melonamin/gt.git"},
		},
		map[string]bool{"fishy": true, "melonamin": true},
	)

	if len(lookups) != 1 {
		t.Fatalf("got %d lookups, want 1", len(lookups))
	}
	lookup := lookups[0]
	if lookup.Branch != "local-fix" || lookup.HeadRef != "fix/123" {
		t.Fatalf("lookup = %#v, want local branch mapped to upstream branch", lookup)
	}
	if lookup.Head != "" {
		t.Fatalf("head = %q, want empty so unpushed local commits do not hide the PR", lookup.Head)
	}
	if !lookup.HeadOwners["fishy"] || len(lookup.HeadOwners) != 1 {
		t.Fatalf("head owners = %#v, want only upstream owner", lookup.HeadOwners)
	}
}

func TestLoadPullRequestStatesFindsTrackedPRWhenLocalBranchIsAhead(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://github.com/fishy/gt.git")
	runGit(t, repoPath, "remote", "add", "upstream", "https://github.com/melonamin/gt.git")
	runGit(t, repoPath, "config", "branch.local-fix.remote", "origin")
	runGit(t, repoPath, "config", "branch.local-fix.merge", "refs/heads/fix/123")

	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		var payload struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if !strings.Contains(payload.Query, `headRefName: "fix/123"`) {
			t.Fatalf("query did not use tracked branch:\n%s", payload.Query)
		}
		response := `{"data":{"repository":{"pr0":{"nodes":[{"state":"OPEN","headRefOid":"pushed-sha","headRepositoryOwner":{"login":"fishy"}}]}}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    req,
		}, nil
	})}

	states, err := loadPullRequestStates(
		context.Background(),
		repoPath,
		[]Worktree{{Branch: "local-fix", Head: "local-unpushed-sha"}},
		func(string) (string, string) { return "token", "test" },
		client,
	)
	if err != nil {
		t.Fatalf("load pull request states: %v", err)
	}
	if states["local-fix"] != pullRequestStateOpen {
		t.Fatalf("state = %q, want %q", states["local-fix"], pullRequestStateOpen)
	}
}

func TestMapPullRequestStatesRequiresHeadAndOwnerMatch(t *testing.T) {
	var response githubPullRequestGraphQLResponse
	response.Data.Repository = map[string]githubPullRequestConnection{
		"pr0": {Nodes: []githubPullRequestNode{
			pullRequestNode("MERGED", "old-sha", "fishy"),
			pullRequestNode("OPEN", "current-sha", "someone-else"),
			pullRequestNode("OPEN", "current-sha", "fishy"),
		}},
		"pr1": {Nodes: []githubPullRequestNode{
			pullRequestNode("CLOSED", "matching-sha", "someone-else"),
		}},
	}

	got := mapPullRequestStates([]pullRequestLookup{
		{Branch: "feature", Head: "current-sha", HeadOwners: map[string]bool{"fishy": true}},
		{Branch: "other", Head: "matching-sha", HeadOwners: map[string]bool{"fishy": true}},
	}, response)

	if got["feature"] != pullRequestStateOpen {
		t.Fatalf("feature state = %q, want %q", got["feature"], pullRequestStateOpen)
	}
	if _, ok := got["other"]; ok {
		t.Fatalf("expected owner mismatch to be omitted")
	}
}

func pullRequestNode(state, head, owner string) githubPullRequestNode {
	var node githubPullRequestNode
	node.State = state
	node.HeadRefOID = head
	node.HeadRepositoryOwner.Login = owner
	return node
}

func TestBuildPullRequestGraphQLQueryIncludesHeadQualifiers(t *testing.T) {
	query := buildPullRequestGraphQLQuery(githubRepo{Owner: "melonamin", Name: "gt"}, []pullRequestLookup{
		{Branch: "local-feature", HeadRef: "feature"},
	})

	for _, want := range []string{"headRefOid", "headRepositoryOwner", "first: 10"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
}

func TestLoadPullRequestStatesAllowsTokenWithoutGHBinary(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://github.com/melonamin/gt.git")

	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatalf("find git: %v", err)
	}
	binDir := t.TempDir()
	if err := os.Symlink(gitPath, filepath.Join(binDir, "git")); err != nil {
		t.Fatalf("symlink git: %v", err)
	}
	t.Setenv("PATH", binDir)

	called := false
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		called = true
		body := `{"data":{"repository":{"pr0":{"nodes":[{"state":"OPEN","headRefOid":"abc123","headRepositoryOwner":{"login":"melonamin"}}]}}}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	got, err := loadPullRequestStates(
		context.Background(),
		repoPath,
		[]Worktree{{Branch: "feature", Head: "abc123"}},
		func(string) (string, string) { return "token", "env" },
		client,
	)
	if err != nil {
		t.Fatalf("loadPullRequestStates: %v", err)
	}
	if !called {
		t.Fatalf("expected GitHub request without gh binary in PATH")
	}
	if got["feature"] != pullRequestStateOpen {
		t.Fatalf("feature state = %q, want %q", got["feature"], pullRequestStateOpen)
	}
}

func TestPullRequestBackoffDecision(t *testing.T) {
	now := time.Unix(100, 0)
	if got := nextPullRequestBackoff(now, nil); !got.IsZero() {
		t.Fatalf("nil error backoff = %v, want zero", got)
	}

	got := nextPullRequestBackoff(now, errors.New("failed"))
	want := now.Add(githubPRBackoff)
	if !got.Equal(want) {
		t.Fatalf("backoff = %v, want %v", got, want)
	}
}

func TestPullRequestSymbolRendering(t *testing.T) {
	if got := pullRequestStateSymbol(pullRequestStateNone); got != " " {
		t.Fatalf("none symbol = %q, want blank", got)
	}
	for _, state := range []pullRequestState{pullRequestStateOpen, pullRequestStateClosed, pullRequestStateMerged} {
		if got := pullRequestStateSymbol(state); got != pullRequestSymbol {
			t.Fatalf("%s symbol = %q, want %q", state, got, pullRequestSymbol)
		}
		if got := renderPullRequestMarker(state); !strings.Contains(got, pullRequestSymbol) {
			t.Fatalf("%s rendered marker = %q, want symbol", state, got)
		}
	}
	if got := renderPullRequestMarker(pullRequestStateNone); got != " " {
		t.Fatalf("none rendered marker = %q, want blank", got)
	}
}

func TestWorktreeViewPlacesPullRequestMarkerAfterBranch(t *testing.T) {
	m := model{
		ui: uiState{width: 120, height: 20},
		wt: worktreeState{filtered: []Worktree{{
			Branch:     "feature",
			Path:       "/repo/.worktrees/feature",
			PRState:    pullRequestStateOpen,
			LastCommit: CommitInfo{Message: "commit"},
		}}},
		repoPath:        "/repo",
		mainWorktreeDir: "/repo",
	}

	view := m.View()
	branchIndex := strings.Index(view, "feature")
	prIndex := strings.Index(view, pullRequestSymbol)
	cleanIndex := strings.Index(view, "✓")
	if branchIndex == -1 || prIndex == -1 || cleanIndex == -1 {
		t.Fatalf("expected branch, PR marker, and clean status in view:\n%s", view)
	}
	if !(branchIndex < prIndex && prIndex < cleanIndex) {
		t.Fatalf("expected branch, PR marker, then clean status; indexes = %d, %d, %d:\n%s", branchIndex, prIndex, cleanIndex, view)
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
