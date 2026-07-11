package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
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

func TestMapPullRequestStates(t *testing.T) {
	var response githubPullRequestGraphQLResponse
	response.Data = map[string]*githubPullRequestRepository{
		"repo0": {PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{pullRequestNode("OPEN", "aaa", "fishy")}}},
		"repo1": {PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{pullRequestNode("CLOSED", "bbb", "fishy")}}},
		"repo2": {Parent: &githubPullRequestRepository{PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{pullRequestNode("MERGED", "ccc", "melonamin")}}}},
		"repo3": {PullRequests: githubPullRequestConnection{}},
	}

	got := mapPullRequestStates([]pullRequestLookup{
		{Branch: "feature", Head: "aaa", HeadOwner: "fishy"},
		{Branch: "bugfix", Head: "bbb", HeadOwner: "fishy"},
		{Branch: "done", Head: "ccc", HeadOwner: "melonamin"},
		{Branch: "empty", Head: "ddd", HeadOwner: "fishy"},
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

func TestUniquePullRequestLookupsUsesConfiguredRemoteIdentity(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "canonical", "https://github.com/fishy/gt.git")
	runGit(t, repoPath, "config", "branch.local-fix.remote", "canonical")
	runGit(t, repoPath, "config", "branch.local-fix.merge", "refs/heads/fix/123")

	lookups := uniquePullRequestLookups(
		context.Background(),
		repoPath,
		[]Worktree{{Branch: "local-fix", Head: "abc123"}},
		[]gitRemote{
			{Name: "canonical", URL: "https://github.com/fishy/gt.git"},
		},
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
	if lookup.HeadRepo != (githubRepo{Owner: "fishy", Name: "gt"}) || lookup.HeadOwner != "fishy" {
		t.Fatalf("lookup = %#v, want configured remote identity", lookup)
	}
}

func TestUniquePullRequestLookupsDoesNotRequireFallbackHeadMatch(t *testing.T) {
	repoPath := initRepo(t)
	remote := gitRemote{Name: "origin", URL: "https://github.com/fishy/gt.git"}

	lookups := uniquePullRequestLookups(
		context.Background(),
		repoPath,
		[]Worktree{{Branch: "feature", Head: "local-diverged-sha"}},
		[]gitRemote{remote},
	)

	if len(lookups) != 1 {
		t.Fatalf("got %d lookups, want 1", len(lookups))
	}
	if lookups[0].Head != "" {
		t.Fatalf("fallback head = %q, want empty so local commits do not hide the PR", lookups[0].Head)
	}
	response := githubPullRequestGraphQLResponse{Data: map[string]*githubPullRequestRepository{
		"repo0": {PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{
			pullRequestNode("OPEN", "remote-pr-sha", "fishy"),
		}}},
	}}
	if got := mapPullRequestStates(lookups, response)["feature"]; got != pullRequestStateOpen {
		t.Fatalf("state = %q, want %q for diverged local branch", got, pullRequestStateOpen)
	}
}

func TestLoadPullRequestStatesFindsTrackedPRWhenLocalBranchIsAhead(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://github.com/fishy/gt.git")
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
		for _, want := range []string{`repository(owner: "fishy", name: "gt")`, "parent {", "pullRequests"} {
			if !strings.Contains(payload.Query, want) {
				t.Fatalf("query missing %q:\n%s", want, payload.Query)
			}
		}
		response := `{"data":{"repo0":{"pullRequests":{"nodes":[]},"parent":{"pullRequests":{"nodes":[{"state":"OPEN","headRefOid":"pushed-sha","headRepositoryOwner":{"login":"fishy"}}]}}}}}`
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
	response.Data = map[string]*githubPullRequestRepository{
		"repo0": {PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{
			pullRequestNode("MERGED", "old-sha", "fishy"),
			pullRequestNode("OPEN", "current-sha", "someone-else"),
			pullRequestNode("OPEN", "current-sha", "fishy"),
		}}},
		"repo1": {PullRequests: githubPullRequestConnection{Nodes: []githubPullRequestNode{
			pullRequestNode("CLOSED", "matching-sha", "someone-else"),
		}}},
	}

	got := mapPullRequestStates([]pullRequestLookup{
		{Branch: "feature", Head: "current-sha", HeadOwner: "fishy"},
		{Branch: "other", Head: "matching-sha", HeadOwner: "fishy"},
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

func TestBuildPullRequestGraphQLQueryIncludesForkParentAndHeadQualifiers(t *testing.T) {
	query := buildPullRequestGraphQLQuery([]pullRequestLookup{
		{Branch: "local-feature", HeadRef: "feature", HeadRepo: githubRepo{Owner: "fishy", Name: "gt"}},
	})

	for _, want := range []string{`repository(owner: "fishy", name: "gt")`, "parent {", "headRefOid", "headRepositoryOwner", "first: 10"} {
		if !strings.Contains(query, want) {
			t.Fatalf("query missing %q:\n%s", want, query)
		}
	}
}

func TestLoadPullRequestStatesUsesProvidedTokenLookupWithoutGHBinary(t *testing.T) {
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
		body := `{"data":{"repo0":{"pullRequests":{"nodes":[{"state":"OPEN","headRefOid":"abc123","headRepositoryOwner":{"login":"melonamin"}}]}}}}`
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

func TestDefaultTokenLookupPrefersEnvironment(t *testing.T) {
	t.Setenv("GH_TOKEN", "token-from-env")
	t.Setenv("GITHUB_TOKEN", "")

	token, _ := defaultTokenLookup("github.com")
	if token != "token-from-env" {
		t.Fatalf("token = %q, want GH_TOKEN value", token)
	}
}

func TestDiscoverPullRequestLookupsCmdSnapshotsWorktrees(t *testing.T) {
	repoPath := initRepo(t)
	runGit(t, repoPath, "remote", "add", "origin", "https://github.com/fishy/gt.git")
	worktrees := []Worktree{{Branch: "feature", Head: "abc123"}}

	cmd := discoverPullRequestLookupsCmd(repoPath, worktrees, 7)
	worktrees[0].Branch = "mutated-after-command-start"
	msg := cmd().(pullRequestLookupsLoadedMsg)

	if msg.err != nil {
		t.Fatalf("discover lookups: %v", msg.err)
	}
	if msg.generation != 7 || len(msg.lookups) != 1 || msg.lookups[0].Branch != "feature" {
		t.Fatalf("message = %#v, want immutable feature snapshot", msg)
	}
}

func TestWorktreesLoadedRebuildsFilteredAndPreservesPullRequestState(t *testing.T) {
	oldLookup := pullRequestLookup{
		Branch: "kept", HeadRef: "kept", HeadOwner: "fishy",
		HeadRepo: githubRepo{Owner: "fishy", Name: "gt"},
	}
	m := model{
		repoPath: "/repo",
		wt: worktreeState{
			worktrees: []Worktree{
				{Branch: "deleted"},
				{Branch: "kept", PRState: pullRequestStateOpen},
			},
			filtered:  []Worktree{{Branch: "deleted"}},
			prLookups: []pullRequestLookup{oldLookup},
		},
	}

	updated, _ := m.Update(worktreesLoadedMsg{worktrees: []Worktree{{Branch: "kept", Head: "abc123"}}})
	m = updated.(model)
	if len(m.wt.filtered) != 1 || m.wt.filtered[0].Branch != "kept" {
		t.Fatalf("filtered = %#v, want only refreshed worktree", m.wt.filtered)
	}
	if got := m.wt.filtered[0].PRState; got != pullRequestStateOpen {
		t.Fatalf("preserved PR state = %q, want %q", got, pullRequestStateOpen)
	}

	updated, _ = m.Update(pullRequestLookupsLoadedMsg{lookups: []pullRequestLookup{oldLookup}, generation: m.wt.prGeneration})
	m = updated.(model)
	if got := m.wt.worktrees[0].PRState; got != pullRequestStateOpen {
		t.Fatalf("reconciled PR state = %q, want cached %q", got, pullRequestStateOpen)
	}
	if m.wt.prFetchAttempts != 0 {
		t.Fatalf("fetch attempts = %d, want no refetch for unchanged lookup", m.wt.prFetchAttempts)
	}
}

func TestPullRequestFetchRetriesOnlyOnce(t *testing.T) {
	m := model{
		repoPath: "/repo",
		wt: worktreeState{
			prGeneration:    1,
			prFetchAttempts: 1,
			prLookups: []pullRequestLookup{{
				Branch: "feature", HeadRef: "feature", HeadRepo: githubRepo{Owner: "fishy", Name: "gt"},
			}},
			worktrees: []Worktree{{Branch: "feature", Head: "abc123", PRState: pullRequestStatePending}},
		},
	}

	updated, cmd := m.Update(pullRequestsLoadedMsg{generation: 1, err: fmt.Errorf("temporary failure")})
	if cmd == nil {
		t.Fatal("expected retry command after the first failure")
	}
	m = updated.(model)
	if got := m.wt.prFetchAttempts; got != 2 {
		t.Fatalf("attempts after retry = %d, want 2", got)
	}
	updated, _ = m.Update(pullRequestsLoadedMsg{generation: 1, err: fmt.Errorf("temporary failure")})
	m = updated.(model)
	if got := m.wt.prFetchAttempts; got != 2 {
		t.Fatalf("attempts after second failure = %d, want no third attempt", got)
	}
}

func TestPullRequestMarkerRendering(t *testing.T) {
	if got := renderPullRequestMarker(pullRequestStatePending); !strings.Contains(got, "?") {
		t.Fatalf("pending marker = %q, want question mark", got)
	}
	for _, state := range []pullRequestState{pullRequestStateOpen, pullRequestStateClosed, pullRequestStateMerged} {
		if got := renderPullRequestMarker(state); !strings.Contains(got, pullRequestSymbol) {
			t.Fatalf("%s rendered marker = %q, want symbol", state, got)
		}
	}
	if got := renderPullRequestMarker(pullRequestStateNone); got != "" {
		t.Fatalf("none rendered marker = %q, want empty", got)
	}
}

func TestRenderWorktreeStatusOmitsMissingPullRequestMarker(t *testing.T) {
	if got := renderWorktreeStatus("✓", pullRequestStateNone); got != "✓" {
		t.Fatalf("status without PR = %q, want clean status only", got)
	}
	if got := renderWorktreeStatus("✓", pullRequestStatePending); !strings.Contains(got, "?") {
		t.Fatalf("status while PR is pending = %q, want question mark", got)
	}
	if got := renderWorktreeStatus("✓", pullRequestStateOpen); !strings.Contains(got, pullRequestSymbol) {
		t.Fatalf("status with open PR = %q, want PR symbol", got)
	}
}

func TestApplyPullRequestStatesLeavesUnresolvedStatePending(t *testing.T) {
	m := model{wt: worktreeState{worktrees: []Worktree{
		{Branch: "has-pr", PRState: pullRequestStatePending},
		{Branch: "no-pr", PRState: pullRequestStatePending},
	}}}

	m.applyPullRequestStates(map[string]pullRequestState{"has-pr": pullRequestStateOpen})
	if got := m.wt.worktrees[0].PRState; got != pullRequestStateOpen {
		t.Fatalf("has-pr state = %q, want %q", got, pullRequestStateOpen)
	}
	if got := m.wt.worktrees[1].PRState; got != pullRequestStatePending {
		t.Fatalf("no-pr state = %q, want pending until its lookup succeeds", got)
	}
}

func TestReconcilePullRequestStatesMarksOnlyNewLookupCandidatesPending(t *testing.T) {
	m := model{wt: worktreeState{worktrees: []Worktree{
		{Branch: "feature", Head: "abc123"},
		{Branch: "detached"},
	}}}

	m.reconcilePullRequestStates([]pullRequestLookup{{Branch: "feature"}})
	if got := m.wt.worktrees[0].PRState; got != pullRequestStatePending {
		t.Fatalf("feature state = %q, want %q", got, pullRequestStatePending)
	}
	if got := m.wt.worktrees[1].PRState; got != pullRequestStateNone {
		t.Fatalf("detached state = %q, want blank", got)
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
	if branchIndex >= cleanIndex || cleanIndex >= prIndex {
		t.Fatalf("expected branch, clean status, then PR marker; indexes = %d, %d, %d:\n%s", branchIndex, cleanIndex, prIndex, view)
	}
}

func TestWorktreeViewKeepsPRMarkerVisibleForLongBranchAndPath(t *testing.T) {
	branch := strings.Repeat("feature-", 12)
	m := model{
		ui: uiState{width: 32, height: 20},
		wt: worktreeState{filtered: []Worktree{{
			Branch:     branch,
			Path:       "/repo/custom-worktree-folder-with-a-very-long-name",
			PRState:    pullRequestStateOpen,
			LastCommit: CommitInfo{Message: "commit"},
		}}},
		repoPath:        "/repo",
		mainWorktreeDir: "/repo",
	}

	for _, line := range strings.Split(m.View(), "\n") {
		if !strings.Contains(line, pullRequestSymbol) {
			continue
		}
		if !strings.Contains(line, "[📂]") {
			t.Fatalf("worktree line dropped folder mismatch marker:\n%s", line)
		}
		if lipgloss.Width(line) > m.ui.width {
			t.Fatalf("worktree line width = %d, want at most %d:\n%s", lipgloss.Width(line), m.ui.width, line)
		}
		return
	}
	t.Fatalf("expected PR marker in view:\n%s", m.View())
}

func TestWorktreeViewDoesNotTruncateBranchWhenThereIsRoom(t *testing.T) {
	branch := strings.Repeat("feature-", 8)
	m := model{
		ui: uiState{width: 160, height: 20},
		wt: worktreeState{filtered: []Worktree{{
			Branch:     branch,
			Path:       "/repo/.worktrees/feature",
			PRState:    pullRequestStateOpen,
			LastCommit: CommitInfo{Message: "commit"},
		}}},
		repoPath:        "/repo",
		mainWorktreeDir: "/repo",
	}

	if view := m.View(); !strings.Contains(view, branch) {
		t.Fatalf("wide view truncated branch %q:\n%s", branch, view)
	}
}

func TestFetchPullRequestStatesUsesPartialGraphQLData(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":{"repo0":{"pullRequests":{"nodes":[{"state":"OPEN","headRefOid":"abc123","headRepositoryOwner":{"login":"fishy"}}]},"parent":null},"repo1":null},"errors":[{"message":"repository not found"}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	states, err := fetchPullRequestStates(context.Background(), client, githubGraphQLEndpoint, "token", []pullRequestLookup{
		{Branch: "feature", Head: "abc123", HeadOwner: "fishy", HeadRepo: githubRepo{Owner: "fishy", Name: "gt"}},
		{Branch: "other", Head: "def456", HeadOwner: "missing", HeadRepo: githubRepo{Owner: "missing", Name: "gt"}},
	})
	if err == nil {
		t.Fatal("expected partial GraphQL error")
	}
	if got := states["feature"]; got != pullRequestStateOpen {
		t.Fatalf("feature state = %q, want %q", got, pullRequestStateOpen)
	}
	if _, ok := states["other"]; ok {
		t.Fatalf("failed alias must remain unresolved, got state %q", states["other"])
	}
}

func TestFetchPullRequestStatesKeepsOnlyFailedAliasPending(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"data":{"repo0":{"pullRequests":{"nodes":[]},"parent":null},"repo1":null},"errors":[{"message":"repository not found","path":["repo1"]}]}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    req,
		}, nil
	})}

	states, err := fetchPullRequestStates(context.Background(), client, githubGraphQLEndpoint, "token", []pullRequestLookup{
		{Branch: "confirmed-none", Head: "abc123", HeadOwner: "fishy", HeadRepo: githubRepo{Owner: "fishy", Name: "gt"}},
		{Branch: "failed", Head: "def456", HeadOwner: "missing", HeadRepo: githubRepo{Owner: "missing", Name: "gt"}},
	})
	if err == nil {
		t.Fatal("expected partial GraphQL error")
	}
	if state, ok := states["confirmed-none"]; !ok || state != pullRequestStateNone {
		t.Fatalf("confirmed alias = %q, present=%v; want resolved no-PR", state, ok)
	}
	if _, ok := states["failed"]; ok {
		t.Fatal("failed alias must not be cleared to no-PR")
	}
}

func TestFetchPullRequestStatesKeepsEarlierBatchResultsWhenLaterBatchFails(t *testing.T) {
	lookups := make([]pullRequestLookup, githubPRBatchSize+1)
	for i := range lookups {
		lookups[i] = pullRequestLookup{
			Branch:   fmt.Sprintf("branch-%d", i),
			HeadRef:  fmt.Sprintf("branch-%d", i),
			HeadRepo: githubRepo{Owner: "fishy", Name: "gt"},
		}
	}
	requests := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		requests++
		if requests == 2 {
			return &http.Response{StatusCode: http.StatusBadGateway, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("bad gateway")), Request: req}, nil
		}
		data := make(map[string]any, githubPRBatchSize)
		for i := 0; i < githubPRBatchSize; i++ {
			data[githubPullRequestRepositoryAlias(i)] = map[string]any{"pullRequests": map[string]any{"nodes": []any{}}, "parent": nil}
		}
		body, err := json.Marshal(map[string]any{"data": data})
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(body))), Request: req}, nil
	})}

	states, err := fetchPullRequestStates(context.Background(), client, githubGraphQLEndpoint, "token", lookups)
	if err == nil {
		t.Fatal("expected failed second batch")
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if got, ok := states["branch-0"]; !ok || got != pullRequestStateNone {
		t.Fatalf("first batch state = %q, present=%v; want resolved no-PR", got, ok)
	}
	if _, ok := states[fmt.Sprintf("branch-%d", githubPRBatchSize)]; ok {
		t.Fatal("failed second batch must remain unresolved")
	}
}

func TestLoadPullRequestStatesSkipsTokenLookupWithoutGitHubCandidates(t *testing.T) {
	repoPath := initRepo(t)
	called := false
	states, err := loadPullRequestStates(context.Background(), repoPath, []Worktree{{Branch: "local", Head: "abc123"}}, func(string) (string, string) {
		called = true
		return "", ""
	}, nil)
	if err != nil {
		t.Fatalf("load pull request states: %v", err)
	}
	if called {
		t.Fatal("token lookup must not run when no GitHub remotes are eligible")
	}
	if len(states) != 0 {
		t.Fatalf("states = %#v, want no PR candidates", states)
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
