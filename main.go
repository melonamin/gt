package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/cli/go-gh/v2/pkg/auth"
)

const (
	version            = "0.5.0"
	defaultWorktreeDir = ".worktrees"
	configFileName     = "config.json"
	configDirName      = "gt"

	// UI constants
	maxCommitMsgLength   = 40
	statusMessageTimeout = 3 * time.Second
	refreshTimeout       = 2 * time.Second
	minViewportHeight    = 5
	viewportPadding      = 8 // Lines for header/footer
	hashDisplayLength    = 7
	branchDisplayWidth   = 20
	ellipsis             = "..."

	// Git command timeouts
	gitCmdTimeout     = 5 * time.Second
	gitCmdSlowTimeout = 10 * time.Second

	// Internal throttling/timeouts
	maxDetailFetchWorkers       = 4
	worktreeDetailTimeout       = 30 * time.Second
	worktreeCmdTimeout          = 2 * time.Minute
	maxPullRequestFetchAttempts = 2
	githubPRBatchSize           = 25
	githubGraphQLEndpoint       = "https://api.github.com/graphql"
	pullRequestSymbol           = "⎇"
)

// Config holds user configuration loaded from ~/.config/gt/config.json.
type Config struct {
	WorktreeDir          string `json:"worktree_dir,omitempty"`
	Shell                string `json:"shell,omitempty"`
	PostCreate           string `json:"post_create,omitempty"`
	DefaultMergeStrategy string `json:"default_merge_strategy,omitempty"` // "squash" or "ff-only"
	GitTimeout           int    `json:"git_timeout,omitempty"`            // Git command timeout in seconds (default: 5)
	GitSlowTimeout       int    `json:"git_slow_timeout,omitempty"`       // Slow git command timeout in seconds (default: 10)
}

// getGitTimeout returns the configured git command timeout or the default.
func (c *Config) getGitTimeout() time.Duration {
	if c != nil && c.GitTimeout > 0 {
		return time.Duration(c.GitTimeout) * time.Second
	}
	return gitCmdTimeout
}

// getGitSlowTimeout returns the configured slow git command timeout or the default.
func (c *Config) getGitSlowTimeout() time.Duration {
	if c != nil && c.GitSlowTimeout > 0 {
		return time.Duration(c.GitSlowTimeout) * time.Second
	}
	return gitCmdSlowTimeout
}

// Worktree represents a git worktree with its associated metadata.
type Worktree struct {
	Path       string
	Branch     string
	Head       string
	IsDirty    bool
	LastCommit CommitInfo
	IsCurrent  bool
	Ahead      int
	Behind     int
	PRState    pullRequestState
}

// CommitInfo holds metadata about a git commit.
type CommitInfo struct {
	Hash    string
	Message string
	Date    time.Time
	Author  string
}

type pullRequestState string

const (
	pullRequestStateNone    pullRequestState = ""
	pullRequestStatePending pullRequestState = "pending"
	pullRequestStateOpen    pullRequestState = "open"
	pullRequestStateClosed  pullRequestState = "closed"
	pullRequestStateMerged  pullRequestState = "merged"
)

type gitRemote struct {
	Name string
	URL  string
}

type githubRepo struct {
	Owner string
	Name  string
}

type pullRequestLookup struct {
	Branch    string
	HeadRef   string
	Head      string
	HeadOwner string
	HeadRepo  githubRepo
}

type tokenLookupFunc func(host string) (token, source string)

func defaultTokenLookup(host string) (string, string) {
	return auth.TokenForHost(host)
}

// inputMode represents the current input state of the TUI.
type inputMode int

const (
	modeNormal        inputMode = iota // Browsing worktrees
	modeSearch                         // Inline search filtering
	modeNewBranch                      // Entering new branch name
	modeNewPath                        // Entering custom worktree path
	modeActions                        // Selecting from actions menu
	modeMergeStrategy                  // Selecting merge strategy
	modeMergeConfirm                   // Confirming merge operation
)

// actionFn is the signature for worktree action handlers.
type actionFn func(wt *Worktree, config *Config) error

// action represents a menu action that can be performed on a worktree.
type action struct {
	key          string
	label        string
	fn           actionFn
	exitAfterRun bool // If false, return to TUI after action completes
	darwinOnly   bool // If true, only available on macOS
}

// uiState holds the UI-related state for the TUI.
type uiState struct {
	cursor       int
	scrollOffset int
	width        int
	height       int
	inputMode    inputMode
	inputValue   string
}

// worktreeState holds worktree-related data.
type worktreeState struct {
	worktrees           []Worktree
	filtered            []Worktree
	searchTerm          string
	defaultBranch       string
	previousWorktree    string
	loading             bool
	loadingMsg          string
	detailQueue         []int
	activeDetailFetches int
	detailGeneration    int
	prGeneration        int
	prFetchAttempts     int
	prLookups           []pullRequestLookup
}

// actionsState holds the state for the actions menu.
type actionsState struct {
	cursor  int
	actions []action
}

// deleteState holds the state for delete confirmation.
type deleteState struct {
	confirming bool
	target     *Worktree
}

// mergeState holds the state for merge-and-delete confirmation flow.
type mergeState struct {
	target   *Worktree
	strategy string // "squash" or "ff-only"
}

// model is the Bubble Tea model for the TUI application.
type model struct {
	ui              uiState
	wt              worktreeState
	acts            actionsState
	del             deleteState
	merge           mergeState
	repoPath        string
	mainWorktreeDir string
	config          *Config
	err             error
	status          struct {
		message string
		isError bool
		timeout time.Time
	}
	quitting bool
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("220")).
			MarginBottom(1)

	searchStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("86"))

	dimStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	selectedStyle = lipgloss.NewStyle().
			Background(lipgloss.Color("236")).
			Bold(true)

	currentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82")).
			Bold(true)

	dirtyStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	branchStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("141")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("240"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	successStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82"))

	aheadStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("82"))

	behindStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("214"))

	pullRequestOpenStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("82"))

	pullRequestClosedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("196"))

	pullRequestMergedStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("141"))
)

// printInfo prints a dimmed informational message to stdout.
func printInfo(format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	fmt.Printf("\n\033[2m%s\033[0m\n", msg)
}

// validateBranchName checks if the given name is valid for a git branch.
func validateBranchName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("branch name cannot be empty")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("branch name cannot start with '-'")
	}
	if strings.HasPrefix(name, ".") {
		return errors.New("branch name cannot start with '.'")
	}
	if strings.HasSuffix(name, ".") {
		return errors.New("branch name cannot end with '.'")
	}
	if strings.HasSuffix(name, ".lock") {
		return errors.New("branch name cannot end with '.lock'")
	}
	if strings.Contains(name, "..") {
		return errors.New("branch name cannot contain '..'")
	}
	if strings.Contains(name, " ") {
		return errors.New("branch name cannot contain spaces")
	}
	if strings.Contains(name, "~") || strings.Contains(name, "^") ||
		strings.Contains(name, ":") || strings.Contains(name, "?") ||
		strings.Contains(name, "*") || strings.Contains(name, "[") ||
		strings.Contains(name, "\\") {
		return errors.New("branch name contains invalid characters")
	}
	// Shell metacharacters that could be dangerous in command execution
	if strings.Contains(name, ";") || strings.Contains(name, "&") ||
		strings.Contains(name, "|") || strings.Contains(name, "$") ||
		strings.Contains(name, "`") || strings.Contains(name, "(") ||
		strings.Contains(name, ")") || strings.Contains(name, "<") ||
		strings.Contains(name, ">") {
		return errors.New("branch name contains invalid characters")
	}
	return nil
}

// truncateString truncates a string to maxLen runes and adds ellipsis if needed.
func truncateString(s string, maxLen int) string {
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen]) + ellipsis
}

// truncateToWidth truncates a string to fit within maxWidth display columns,
// adding an ellipsis suffix if truncation is needed.
func truncateToWidth(s string, maxWidth int) string {
	if maxWidth <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxWidth {
		return s
	}
	ellipsisWidth := lipgloss.Width(ellipsis)
	if maxWidth <= ellipsisWidth {
		return ellipsis[:maxWidth]
	}
	runes := []rune(s)
	width := 0
	for i, r := range runes {
		rw := lipgloss.Width(string(r))
		if width+rw > maxWidth-ellipsisWidth {
			return string(runes[:i]) + ellipsis
		}
		width += rw
	}
	return s
}

// isExpectedWorktreePath checks whether the worktree folder name matches
// the convention for the given branch (slashes replaced with dashes).
func isExpectedWorktreePath(branch, worktreePath string) bool {
	if branch == "" || worktreePath == "" {
		return true
	}
	expectedName := strings.ReplaceAll(branch, "/", "-")
	actualName := filepath.Base(filepath.Clean(worktreePath))
	return actualName == expectedName
}

func normalizeGitHubRepoName(repo string) string {
	return strings.TrimSuffix(strings.TrimSpace(repo), ".git")
}

func parseGitHubRemoteURL(remoteURL string) (githubRepo, bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return githubRepo{}, false
	}

	parsePath := func(path string) (githubRepo, bool) {
		path = strings.Trim(path, "/")
		parts := strings.Split(path, "/")
		if len(parts) < 2 {
			return githubRepo{}, false
		}
		owner := strings.TrimSpace(parts[0])
		name := normalizeGitHubRepoName(parts[1])
		if owner == "" || name == "" {
			return githubRepo{}, false
		}
		return githubRepo{Owner: owner, Name: name}, true
	}

	if u, err := url.Parse(remoteURL); err == nil && u.Scheme != "" {
		if strings.EqualFold(u.Hostname(), "github.com") {
			return parsePath(u.Path)
		}
		return githubRepo{}, false
	}

	if strings.HasPrefix(remoteURL, "git@github.com:") {
		return parsePath(strings.TrimPrefix(remoteURL, "git@github.com:"))
	}

	return githubRepo{}, false
}

func pullRequestStateFromGitHub(state string) pullRequestState {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "OPEN":
		return pullRequestStateOpen
	case "CLOSED":
		return pullRequestStateClosed
	case "MERGED":
		return pullRequestStateMerged
	default:
		return pullRequestStateNone
	}
}

func renderPullRequestMarker(state pullRequestState) string {
	switch state {
	case pullRequestStatePending:
		return dimStyle.Render("?")
	case pullRequestStateOpen:
		return pullRequestOpenStyle.Render(pullRequestSymbol)
	case pullRequestStateClosed:
		return pullRequestClosedStyle.Render(pullRequestSymbol)
	case pullRequestStateMerged:
		return pullRequestMergedStyle.Render(pullRequestSymbol)
	default:
		return ""
	}
}

func renderWorktreeStatus(status string, pullRequestState pullRequestState) string {
	pullRequestMarker := renderPullRequestMarker(pullRequestState)
	if pullRequestMarker == "" {
		return status
	}
	return status + " " + pullRequestMarker
}

func getConfigPath() string {
	if xdgConfigHome := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdgConfigHome != "" {
		return filepath.Join(xdgConfigHome, configDirName, configFileName)
	}

	configHome, err := os.UserConfigDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".config", configDirName, configFileName)
	}
	return filepath.Join(configHome, configDirName, configFileName)
}

func loadConfig() (*Config, error) {
	configPath := getConfigPath()
	data, err := os.ReadFile(configPath)
	if err != nil {
		if os.IsNotExist(err) {
			return &Config{}, nil
		}
		return nil, err
	}

	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return &Config{}, nil
	}

	return &config, nil
}

func saveConfig(config *Config) error {
	configPath := getConfigPath()
	configDir := filepath.Dir(configPath)

	if err := os.MkdirAll(configDir, 0755); err != nil {
		return err
	}

	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(configPath, data, 0644)
}

func getCurrentRepoPath() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("not in a git repository")
	}
	return strings.TrimSpace(string(output)), nil
}

func getMainWorktreePath(repoPath string) (string, bool) {
	output, err := runGitCmdCombinedWithTimeout(repoPath, gitCmdTimeout, "worktree", "list", "--porcelain")
	if err != nil {
		return "", false
	}

	var mainWorktreePath string
	for _, line := range strings.Split(string(output), "\n") {
		if path, found := strings.CutPrefix(line, "worktree "); found {
			mainWorktreePath = filepath.Clean(path)
			break
		}
	}
	if mainWorktreePath == "" {
		return "", false
	}

	// Only recognize the conventional layout: a non-empty checkout with its
	// Git directory at <worktree>/.git. Ambiguous layouts fall back to the
	// current Git top-level.
	if info, err := os.Stat(filepath.Join(mainWorktreePath, ".git")); err != nil || !info.IsDir() {
		return "", false
	}
	entries, err := os.ReadDir(mainWorktreePath)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if entry.Name() != ".git" {
			return mainWorktreePath, true
		}
	}
	return "", false
}

func configuredWorktreeDir(config *Config) string {
	if config != nil && config.WorktreeDir != "" {
		return config.WorktreeDir
	}
	return defaultWorktreeDir
}

func resolveWorktreeDir(repoPath string, config *Config) (worktreeDir, baseRepoPath string) {
	worktreeDir = configuredWorktreeDir(config)
	if filepath.IsAbs(worktreeDir) {
		return filepath.Clean(worktreeDir), repoPath
	}

	baseRepoPath = repoPath
	if mainWorktreePath, ok := getMainWorktreePath(repoPath); ok {
		baseRepoPath = mainWorktreePath
	}
	return filepath.Join(baseRepoPath, worktreeDir), baseRepoPath
}

func relPathWithin(basePath, targetPath string) (string, bool) {
	relPath, err := filepath.Rel(basePath, targetPath)
	if err != nil || relPath == "" || relPath == "." || filepath.IsAbs(relPath) {
		return "", false
	}
	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relPath, true
}

func ensureWorktreeDir(repoPath string, config *Config) (string, error) {
	worktreeDir, baseRepoPath := resolveWorktreeDir(repoPath, config)
	if filepath.Clean(worktreeDir) == filepath.Clean(baseRepoPath) {
		return "", fmt.Errorf("worktree directory must not be the repository root")
	}

	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		return "", err
	}

	if relPath, ok := relPathWithin(baseRepoPath, worktreeDir); ok {
		if err := ensureNoTrackedFiles(baseRepoPath, relPath); err != nil {
			return "", err
		}
		if err := ensureGitExcludeEntry(baseRepoPath, relPath); err != nil {
			// Don't fail the worktree creation if we can't update git excludes.
			fmt.Fprintf(os.Stderr, "Warning: Could not update git excludes: %v\n", err)
		}
	}

	return worktreeDir, nil
}

// runGitCmd executes a git command with context support for cancellation.
func runGitCmd(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("git command timed out: git %s", strings.Join(args, " "))
	}
	return output, err
}

func runGitCmdCombined(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		return output, fmt.Errorf("git command timed out: git %s", strings.Join(args, " "))
	}
	return output, err
}

func runGitCmdCombinedWithTimeout(dir string, timeout time.Duration, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return runGitCmdCombined(ctx, dir, args...)
}

// getAheadBehindWithContext returns the number of commits the branch is ahead/behind the default branch.
// Returns (0, 0) if the branch is the default branch, or if any error occurs during git operations.
func getAheadBehindWithContext(ctx context.Context, worktreePath, branch, defaultBranch string) (ahead, behind int) {
	if branch == "" || branch == defaultBranch {
		return 0, 0
	}

	// Get count of commits ahead and behind
	output, err := runGitCmd(ctx, worktreePath, "rev-list", "--left-right", "--count", fmt.Sprintf("%s...%s", defaultBranch, branch))
	if err != nil {
		// Git command failed (e.g., branch doesn't exist in remote, or not a valid ref)
		// Silently return 0,0 as this is a non-critical display feature
		return 0, 0
	}

	parts := strings.Fields(strings.TrimSpace(string(output)))
	if len(parts) != 2 {
		// Unexpected output format
		return 0, 0
	}

	behindVal, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0
	}
	aheadVal, err := strconv.Atoi(parts[1])
	if err != nil {
		return 0, 0
	}

	return aheadVal, behindVal
}

// getAheadBehind returns the number of commits the branch is ahead/behind the default branch.
// Returns (0, 0) if the branch is the default branch, or if any error occurs during git operations.
func getAheadBehind(worktreePath, branch, defaultBranch string) (ahead, behind int) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	return getAheadBehindWithContext(ctx, worktreePath, branch, defaultBranch)
}

// worktreeDetails holds the fetched details for a single worktree.
type worktreeDetails struct {
	index      int
	isDirty    bool
	lastCommit CommitInfo
	ahead      int
	behind     int
}

// collectWorktreeDetail fetches status, commit info, and ahead/behind for a single worktree.
// ctx must not be nil.
func collectWorktreeDetail(ctx context.Context, wt Worktree, index int, defaultBranch string) worktreeDetails {
	result := worktreeDetails{index: index}

	// Check if dirty
	output, err := runGitCmd(ctx, wt.Path, "status", "--porcelain")
	if err == nil {
		result.isDirty = len(strings.TrimSpace(string(output))) > 0
	}

	// Get last commit info
	output, err = runGitCmd(ctx, wt.Path, "log", "-1", "--pretty=format:%H|%s|%ai|%an")
	if err == nil && len(output) > 0 {
		parts := strings.Split(string(output), "|")
		if len(parts) >= 4 {
			hash := parts[0]
			if len(hash) > hashDisplayLength {
				hash = hash[:hashDisplayLength]
			}
			result.lastCommit.Hash = hash
			result.lastCommit.Message = parts[1]
			if t, err := time.Parse("2006-01-02 15:04:05 -0700", parts[2]); err == nil {
				result.lastCommit.Date = t
			}
			result.lastCommit.Author = parts[3]
		}
	}

	// Get ahead/behind count relative to default branch
	result.ahead, result.behind = getAheadBehindWithContext(ctx, wt.Path, wt.Branch, defaultBranch)

	return result
}

// listWorktreesWithContext fetches basic worktree metadata without per-worktree git status calls.
func listWorktreesWithContext(ctx context.Context, repoPath string) ([]Worktree, string, error) {
	output, err := runGitCmd(ctx, repoPath, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, "", err
	}

	// Get default branch early for ahead/behind calculation
	defaultBranch := getDefaultBranchWithContext(ctx, repoPath)

	var worktrees []Worktree
	lines := strings.Split(string(output), "\n")

	var current Worktree
	for _, line := range lines {
		if strings.HasPrefix(line, "worktree ") {
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = Worktree{
				Path: strings.TrimPrefix(line, "worktree "),
			}
		} else if strings.HasPrefix(line, "HEAD ") {
			current.Head = strings.TrimPrefix(line, "HEAD ")
		} else if strings.HasPrefix(line, "branch ") {
			current.Branch = strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(current.Branch, "refs/heads/")
		} else if line == "" && current.Path != "" {
			worktrees = append(worktrees, current)
			current = Worktree{}
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}

	// Get current directory to mark current worktree
	cwd, _ := os.Getwd()
	for i := range worktrees {
		worktrees[i].IsCurrent = strings.HasPrefix(cwd, worktrees[i].Path)
	}

	return worktrees, defaultBranch, nil
}

// populateWorktreeDetails fills in expensive metadata for each worktree sequentially.
func populateWorktreeDetails(ctx context.Context, worktrees []Worktree, defaultBranch string) []Worktree {
	if len(worktrees) == 0 {
		return worktrees
	}

	parentCtx := ctx
	if parentCtx == nil {
		parentCtx = context.Background()
	}

	for i := range worktrees {
		detailCtx, cancel := context.WithTimeout(parentCtx, worktreeDetailTimeout)
		detail := collectWorktreeDetail(detailCtx, worktrees[i], i, defaultBranch)
		cancel()
		worktrees[i].IsDirty = detail.isDirty
		worktrees[i].LastCommit = detail.lastCommit
		worktrees[i].Ahead = detail.ahead
		worktrees[i].Behind = detail.behind
	}

	return worktrees
}

// getWorktreesWithContext fetches all worktrees along with their details.
func getWorktreesWithContext(ctx context.Context, repoPath string) ([]Worktree, string, error) {
	worktrees, defaultBranch, err := listWorktreesWithContext(ctx, repoPath)
	if err != nil {
		return nil, "", err
	}
	if ctx != nil && ctx.Err() != nil {
		return worktrees, defaultBranch, ctx.Err()
	}
	worktrees = populateWorktreeDetails(ctx, worktrees, defaultBranch)
	return worktrees, defaultBranch, nil
}

func getWorktrees(repoPath string) ([]Worktree, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdSlowTimeout)
	defer cancel()
	worktrees, _, err := getWorktreesWithContext(ctx, repoPath)
	return worktrees, err
}

func filterWorktrees(worktrees []Worktree, search string) []Worktree {
	if search == "" {
		return worktrees
	}

	search = strings.ToLower(search)
	var filtered []Worktree
	for _, wt := range worktrees {
		if strings.Contains(strings.ToLower(wt.Branch), search) ||
			strings.Contains(strings.ToLower(wt.LastCommit.Message), search) ||
			strings.Contains(strings.ToLower(wt.Path), search) {
			filtered = append(filtered, wt)
		}
	}
	return filtered
}

func formatRelativeTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	now := time.Now()
	diff := now.Sub(t)

	switch {
	case diff < time.Minute:
		return "just now"
	case diff < time.Hour:
		mins := int(diff.Minutes())
		if mins == 1 {
			return "1 minute ago"
		}
		return fmt.Sprintf("%d minutes ago", mins)
	case diff < 24*time.Hour:
		hours := int(diff.Hours())
		if hours == 1 {
			return "1 hour ago"
		}
		return fmt.Sprintf("%d hours ago", hours)
	case diff < 7*24*time.Hour:
		days := int(diff.Hours() / 24)
		if days == 1 {
			return "1 day ago"
		}
		return fmt.Sprintf("%d days ago", days)
	default:
		return t.Format("Jan 2, 2006")
	}
}

// getActions returns the list of available worktree actions, filtered by platform.
func getActions() []action {
	allActions := []action{
		{
			key:          "e",
			label:        "Open in editor ($EDITOR)",
			exitAfterRun: true,
			fn: func(wt *Worktree, config *Config) error {
				editor := os.Getenv("EDITOR")
				if editor == "" {
					editor = "vim"
				}
				cmd := exec.Command(editor, wt.Path)
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			},
		},
		{
			key:          "c",
			label:        "Open in VS Code",
			exitAfterRun: true,
			fn: func(wt *Worktree, config *Config) error {
				if _, err := exec.LookPath("code"); err != nil {
					return errors.New("VS Code not found (command 'code' not in PATH)")
				}
				cmd := exec.Command("code", wt.Path)
				return cmd.Run()
			},
		},
		{
			key:          "t",
			label:        "Open terminal here",
			exitAfterRun: true,
			fn: func(wt *Worktree, config *Config) error {
				shell := getShell(config)
				cmd := exec.Command(shell)
				cmd.Dir = wt.Path
				cmd.Stdin = os.Stdin
				cmd.Stdout = os.Stdout
				cmd.Stderr = os.Stderr
				return cmd.Run()
			},
		},
		{
			key:          "f",
			label:        "Open in Finder",
			exitAfterRun: false, // Can stay in TUI
			darwinOnly:   true,
			fn: func(wt *Worktree, config *Config) error {
				cmd := exec.Command("open", wt.Path)
				return cmd.Run()
			},
		},
		{
			key:          "p",
			label:        "Copy path to clipboard",
			exitAfterRun: false, // Stay in TUI after copy
			darwinOnly:   true,
			fn: func(wt *Worktree, config *Config) error {
				cmd := exec.Command("pbcopy")
				cmd.Stdin = strings.NewReader(wt.Path)
				return cmd.Run()
			},
		},
	}

	// Filter actions by platform
	if runtime.GOOS == "darwin" {
		return allActions
	}

	var filtered []action
	for _, a := range allActions {
		if !a.darwinOnly {
			filtered = append(filtered, a)
		}
	}
	return filtered
}

func initialModel() model {
	repoPath, err := getCurrentRepoPath()
	if err != nil {
		return model{err: err}
	}

	config, _ := loadConfig()
	if config == nil {
		config = &Config{}
	}

	// Return immediately with loading state - actual data fetch happens in Init()
	return model{
		ui: uiState{},
		wt: worktreeState{
			loading:    true,
			loadingMsg: "Loading worktrees...",
		},
		acts: actionsState{
			actions: getActions(),
		},
		repoPath: repoPath,
		config:   config,
	}
}

func (m model) Init() tea.Cmd {
	if m.err != nil {
		return nil
	}
	// Start async worktree loading
	return loadWorktreesCmd(m.repoPath)
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// worktreesLoadedMsg is sent when async worktree loading completes.
type worktreesLoadedMsg struct {
	worktrees       []Worktree
	defaultBranch   string
	mainWorktreeDir string
	err             error
}

// worktreeDetailLoadedMsg reports incremental detail updates for a single worktree.
type worktreeDetailLoadedMsg struct {
	detail     worktreeDetails
	generation int
}

type pullRequestsLoadedMsg struct {
	states     map[string]pullRequestState
	generation int
	err        error
}

// pullRequestLookupsLoadedMsg is sent once GitHub-eligible branches have been
// identified. Keeping this separate from the network request means branches
// which cannot possibly have a GitHub PR never briefly show an unavailable
// marker.
type pullRequestLookupsLoadedMsg struct {
	lookups    []pullRequestLookup
	generation int
	err        error
}

func fetchWorktreeDetailCmd(wt Worktree, index int, defaultBranch string, generation int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), worktreeDetailTimeout)
		defer cancel()
		detail := collectWorktreeDetail(ctx, wt, index, defaultBranch)
		return worktreeDetailLoadedMsg{
			detail:     detail,
			generation: generation,
		}
	}
}

func discoverPullRequestLookupsCmd(repoPath string, worktrees []Worktree, generation int) tea.Cmd {
	// Bubble Tea commands run asynchronously. Keep their input independent from
	// the model's slice, whose worktree details are updated on the main loop.
	worktreeSnapshot := append([]Worktree(nil), worktrees...)
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitCmdSlowTimeout)
		defer cancel()
		lookups, err := loadPullRequestLookups(ctx, repoPath, worktreeSnapshot)
		return pullRequestLookupsLoadedMsg{
			lookups:    lookups,
			generation: generation,
			err:        err,
		}
	}
}

func fetchPullRequestsCmd(lookups []pullRequestLookup, generation int) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitCmdSlowTimeout)
		defer cancel()
		states, err := loadPullRequestStatesForLookups(ctx, lookups, defaultTokenLookup, http.DefaultClient)
		return pullRequestsLoadedMsg{states: states, generation: generation, err: err}
	}
}

type worktreeCreatedMsg struct {
	branch string
	err    error
}

func createWorktreeCmd(repoPath, branch string, config *Config) tea.Cmd {
	var cfg *Config
	if config != nil {
		copy := *config
		cfg = &copy
	}
	return func() tea.Msg {
		_, err := createWorktree(repoPath, branch, cfg)
		return worktreeCreatedMsg{
			branch: branch,
			err:    err,
		}
	}
}

// loadWorktreesCmd creates a command that loads worktrees asynchronously.
func loadWorktreesCmd(repoPath string) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), gitCmdSlowTimeout)
		defer cancel()
		worktrees, defaultBranch, err := listWorktreesWithContext(ctx, repoPath)
		var mainDir string
		if len(worktrees) > 0 {
			mainDir = worktrees[0].Path
		}
		return worktreesLoadedMsg{
			worktrees:       worktrees,
			defaultBranch:   defaultBranch,
			mainWorktreeDir: mainDir,
			err:             err,
		}
	}
}

// setStatus sets a status message that will be displayed temporarily.
func (m *model) setStatus(msg string, timeout time.Duration) {
	m.status.message = msg
	m.status.isError = false
	m.status.timeout = time.Now().Add(timeout)
}

// setErrorStatus sets an error status message that will be displayed temporarily.
func (m *model) setErrorStatus(msg string, timeout time.Duration) {
	m.status.message = msg
	m.status.isError = true
	m.status.timeout = time.Now().Add(timeout)
}

// refreshWorktrees sets loading state and returns a command to reload the worktree list.
func (m *model) refreshWorktrees() tea.Cmd {
	m.wt.loading = true
	m.wt.loadingMsg = "Refreshing..."
	m.wt.detailQueue = nil
	m.wt.activeDetailFetches = 0
	return loadWorktreesCmd(m.repoPath)
}

func (m *model) resetDetailQueue() {
	m.wt.detailQueue = m.wt.detailQueue[:0]
	for i := range m.wt.worktrees {
		m.wt.detailQueue = append(m.wt.detailQueue, i)
	}
	m.wt.activeDetailFetches = 0
}

func (m *model) startDetailFetches() tea.Cmd {
	if len(m.wt.detailQueue) == 0 {
		return nil
	}

	maxWorkers := maxDetailFetchWorkers
	cmds := make([]tea.Cmd, 0, maxWorkers)
	for m.wt.activeDetailFetches < maxWorkers && len(m.wt.detailQueue) > 0 {
		idx := m.wt.detailQueue[0]
		m.wt.detailQueue = m.wt.detailQueue[1:]
		if idx < 0 || idx >= len(m.wt.worktrees) {
			continue
		}
		wt := m.wt.worktrees[idx]
		cmds = append(cmds, fetchWorktreeDetailCmd(wt, idx, m.wt.defaultBranch, m.wt.detailGeneration))
		m.wt.activeDetailFetches++
	}
	if len(cmds) == 0 {
		return nil
	}
	return tea.Batch(cmds...)
}

func (m *model) startPullRequestFetch() tea.Cmd {
	if m.wt.prFetchAttempts >= maxPullRequestFetchAttempts {
		return nil
	}
	lookups := make([]pullRequestLookup, 0, len(m.wt.prLookups))
	for _, lookup := range m.wt.prLookups {
		for _, wt := range m.wt.worktrees {
			if wt.Branch == lookup.Branch && wt.PRState == pullRequestStatePending {
				lookups = append(lookups, lookup)
				break
			}
		}
	}
	if len(lookups) == 0 {
		return nil
	}
	m.wt.prFetchAttempts++
	return fetchPullRequestsCmd(lookups, m.wt.prGeneration)
}

func (m *model) applyWorktreeDetail(detail worktreeDetails) {
	if detail.index < 0 || detail.index >= len(m.wt.worktrees) {
		return
	}

	wt := &m.wt.worktrees[detail.index]
	wt.IsDirty = detail.isDirty
	wt.LastCommit = detail.lastCommit
	wt.Ahead = detail.ahead
	wt.Behind = detail.behind

	// Rebuild filtered from worktrees to keep them in sync
	// This is O(n) but simpler and more correct than manual sync
	m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
}

func (m *model) applyPullRequestStates(states map[string]pullRequestState) {
	for i := range m.wt.worktrees {
		if state, ok := states[m.wt.worktrees[i].Branch]; ok {
			m.wt.worktrees[i].PRState = state
		}
	}
	m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
}

func pullRequestLookupSetByBranch(lookups []pullRequestLookup) map[string]map[pullRequestLookup]bool {
	sets := make(map[string]map[pullRequestLookup]bool)
	for _, lookup := range lookups {
		if sets[lookup.Branch] == nil {
			sets[lookup.Branch] = make(map[pullRequestLookup]bool)
		}
		sets[lookup.Branch][lookup] = true
	}
	return sets
}

func equalPullRequestLookupSets(a, b map[pullRequestLookup]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for lookup := range a {
		if !b[lookup] {
			return false
		}
	}
	return true
}

func (m *model) reconcilePullRequestStates(lookups []pullRequestLookup) {
	candidates := make(map[string]bool, len(lookups))
	for _, lookup := range lookups {
		candidates[lookup.Branch] = true
	}
	oldLookups := pullRequestLookupSetByBranch(m.wt.prLookups)
	newLookups := pullRequestLookupSetByBranch(lookups)
	for i := range m.wt.worktrees {
		wt := &m.wt.worktrees[i]
		if !candidates[wt.Branch] {
			wt.PRState = pullRequestStateNone
		} else if !equalPullRequestLookupSets(oldLookups[wt.Branch], newLookups[wt.Branch]) {
			wt.PRState = pullRequestStatePending
		}
	}
	m.wt.prLookups = lookups
	m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
}

// clampScroll adjusts scroll offset to keep the cursor visible within the viewport.
func (m *model) clampScroll() {
	viewportHeight := m.ui.height - viewportPadding
	if viewportHeight < minViewportHeight {
		viewportHeight = minViewportHeight
	}

	if m.ui.cursor >= m.ui.scrollOffset+viewportHeight {
		m.ui.scrollOffset = m.ui.cursor - viewportHeight + 1
	} else if m.ui.cursor < m.ui.scrollOffset {
		m.ui.scrollOffset = m.ui.cursor
	}
}

// handleDeleteConfirm handles key input during delete confirmation.
func (m model) handleDeleteConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		var refreshCmd tea.Cmd
		if m.del.target != nil {
			if err := deleteWorktree(m.repoPath, m.del.target.Path); err != nil {
				m.setErrorStatus(fmt.Sprintf("Delete failed: %v", err), statusMessageTimeout)
			} else {
				m.setStatus(fmt.Sprintf("Deleted worktree: %s", m.del.target.Branch), statusMessageTimeout)
				refreshCmd = m.refreshWorktrees()
			}
		}
		m.del.confirming = false
		m.del.target = nil
		return m, tea.Batch(tickCmd(), refreshCmd)
	case "n", "N", "esc", "ctrl+c":
		m.del.confirming = false
		m.del.target = nil
		return m, nil
	}
	return m, nil
}

// handleSearchMode handles key input during search mode.
func (m model) handleSearchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.ui.inputMode = modeNormal
		m.wt.searchTerm = ""
		m.wt.filtered = m.wt.worktrees
		return m, nil
	case "enter":
		m.ui.inputMode = modeNormal
		return m, nil
	case "backspace":
		if len(m.wt.searchTerm) > 0 {
			m.wt.searchTerm = m.wt.searchTerm[:len(m.wt.searchTerm)-1]
			m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
		}
		return m, nil
	default:
		if len(msg.String()) == 1 {
			m.wt.searchTerm += msg.String()
			m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
		}
		return m, nil
	}
}

// handleNewBranchMode handles key input during new branch creation.
func (m model) handleNewBranchMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c":
		m.ui.inputMode = modeNormal
		m.ui.inputValue = ""
		return m, nil
	case "enter":
		var createCmd tea.Cmd
		if m.ui.inputValue != "" {
			if err := validateBranchName(m.ui.inputValue); err != nil {
				m.setErrorStatus(fmt.Sprintf("Invalid branch name: %v", err), statusMessageTimeout)
			} else {
				m.wt.loading = true
				m.wt.loadingMsg = fmt.Sprintf("Creating %s...", m.ui.inputValue)
				createCmd = createWorktreeCmd(m.repoPath, m.ui.inputValue, m.config)
			}
		}
		m.ui.inputMode = modeNormal
		m.ui.inputValue = ""
		if createCmd != nil {
			return m, tea.Batch(tickCmd(), createCmd)
		}
		return m, tickCmd()
	case "backspace":
		if len(m.ui.inputValue) > 0 {
			m.ui.inputValue = m.ui.inputValue[:len(m.ui.inputValue)-1]
		}
		return m, nil
	default:
		if len(msg.String()) == 1 || msg.String() == "/" || msg.String() == "-" {
			m.ui.inputValue += msg.String()
		}
		return m, nil
	}
}

// runAction executes an action and handles whether to exit or stay in TUI.
func (m model) runAction(action action, wt *Worktree) (tea.Model, tea.Cmd) {
	m.ui.inputMode = modeNormal

	if action.exitAfterRun {
		m.quitting = true
		printInfo("Running: %s", action.label)
		if err := action.fn(wt, m.config); err != nil {
			fmt.Fprintf(os.Stderr, "Action failed: %v\n", err)
		}
		return m, tea.Quit
	}

	// Non-exiting action: run and show status
	if err := action.fn(wt, m.config); err != nil {
		m.setErrorStatus(fmt.Sprintf("Action failed: %v", err), statusMessageTimeout)
	} else {
		m.setStatus(fmt.Sprintf("%s completed", action.label), refreshTimeout)
	}
	return m, tickCmd()
}

// handleActionsMode handles key input during action selection.
func (m model) handleActionsMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.ui.inputMode = modeNormal
		return m, nil
	case "up", "k":
		if m.acts.cursor > 0 {
			m.acts.cursor--
		}
		return m, nil
	case "down", "j":
		if m.acts.cursor < len(m.acts.actions)-1 {
			m.acts.cursor++
		}
		return m, nil
	case "enter":
		if m.ui.cursor < len(m.wt.filtered) && m.acts.cursor < len(m.acts.actions) {
			wt := m.wt.filtered[m.ui.cursor]
			action := m.acts.actions[m.acts.cursor]
			return m.runAction(action, &wt)
		}
		return m, nil
	default:
		// Check for action shortcut keys
		for _, action := range m.acts.actions {
			if msg.String() == action.key {
				if m.ui.cursor < len(m.wt.filtered) {
					wt := m.wt.filtered[m.ui.cursor]
					return m.runAction(action, &wt)
				}
			}
		}
		return m, nil
	}
}

// handleMergeStrategyMode handles key input during merge strategy selection.
func (m model) handleMergeStrategyMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.ui.inputMode = modeNormal
		m.merge.target = nil
		m.merge.strategy = ""
		return m, nil
	case "s", "S":
		m.merge.strategy = "squash"
		m.ui.inputMode = modeMergeConfirm
		return m, nil
	case "f", "F":
		m.merge.strategy = "ff-only"
		m.ui.inputMode = modeMergeConfirm
		return m, nil
	}
	return m, nil
}

// handleMergeConfirmMode handles key input during merge confirmation.
func (m model) handleMergeConfirmMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		var refreshCmd tea.Cmd
		if m.merge.target != nil {
			if err := mergeAndDeleteWorktree(m.repoPath, m.merge.target, m.wt.defaultBranch, m.merge.strategy); err != nil {
				m.setErrorStatus(fmt.Sprintf("Merge failed: %v", err), statusMessageTimeout)
			} else {
				m.setStatus(fmt.Sprintf("Merged '%s' into '%s' and deleted worktree", m.merge.target.Branch, m.wt.defaultBranch), statusMessageTimeout)
				refreshCmd = m.refreshWorktrees()
			}
		}
		m.ui.inputMode = modeNormal
		m.merge.target = nil
		m.merge.strategy = ""
		return m, tea.Batch(tickCmd(), refreshCmd)
	case "n", "N", "esc", "ctrl+c":
		m.ui.inputMode = modeNormal
		m.merge.target = nil
		m.merge.strategy = ""
		return m, nil
	}
	return m, nil
}

// handleNormalMode handles key input during normal browsing mode.
func (m model) handleNormalMode(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.ui.cursor > 0 {
			m.ui.cursor--
			m.clampScroll()
		}
		return m, nil

	case "down", "j":
		if m.ui.cursor < len(m.wt.filtered)-1 {
			m.ui.cursor++
			m.clampScroll()
		}
		return m, nil

	case "/":
		m.ui.inputMode = modeSearch
		m.wt.searchTerm = ""
		return m, nil

	case "n":
		m.ui.inputMode = modeNewBranch
		m.ui.inputValue = ""
		return m, nil

	case "d":
		if m.ui.cursor < len(m.wt.filtered) {
			wt := &m.wt.filtered[m.ui.cursor]
			if strings.HasSuffix(m.repoPath, wt.Path) {
				m.setErrorStatus("Cannot delete main worktree", statusMessageTimeout)
				return m, tickCmd()
			}
			m.del.confirming = true
			m.del.target = wt
		}
		return m, nil

	case "r":
		refreshCmd := m.refreshWorktrees()
		return m, tea.Batch(tickCmd(), refreshCmd)

	case "^":
		for i, wt := range m.wt.filtered {
			if wt.Branch == m.wt.defaultBranch {
				m.ui.cursor = i
				m.clampScroll()
				m.setStatus(fmt.Sprintf("Jumped to default branch: %s", m.wt.defaultBranch), refreshTimeout)
				break
			}
		}
		return m, tickCmd()

	case "-":
		if m.wt.previousWorktree != "" {
			for i, wt := range m.wt.filtered {
				if wt.Path == m.wt.previousWorktree {
					m.ui.cursor = i
					m.clampScroll()
					m.setStatus("Jumped to previous worktree (session)", refreshTimeout)
					break
				}
			}
		}
		return m, tickCmd()

	case "@":
		for i, wt := range m.wt.filtered {
			if wt.IsCurrent {
				m.ui.cursor = i
				m.clampScroll()
				m.setStatus("Jumped to current worktree", refreshTimeout)
				break
			}
		}
		return m, tickCmd()

	case "a":
		if m.ui.cursor < len(m.wt.filtered) {
			m.ui.inputMode = modeActions
			m.acts.cursor = 0
		}
		return m, nil

	case "m":
		if m.ui.cursor < len(m.wt.filtered) {
			wt := &m.wt.filtered[m.ui.cursor]
			// Validate before showing strategy picker
			if strings.HasSuffix(m.repoPath, wt.Path) {
				m.setErrorStatus("Cannot merge main worktree", statusMessageTimeout)
				return m, tickCmd()
			}
			if wt.Branch == m.wt.defaultBranch {
				m.setErrorStatus("Cannot merge default branch into itself", statusMessageTimeout)
				return m, tickCmd()
			}
			if wt.IsCurrent {
				m.setErrorStatus("Cannot merge currently checked out worktree", statusMessageTimeout)
				return m, tickCmd()
			}
			m.merge.target = wt
			m.ui.inputMode = modeMergeStrategy
		}
		return m, nil

	case "enter":
		if m.ui.cursor < len(m.wt.filtered) {
			wt := m.wt.filtered[m.ui.cursor]

			// Track previous worktree before switching (session-only)
			for _, w := range m.wt.worktrees {
				if w.IsCurrent {
					m.wt.previousWorktree = w.Path
					break
				}
			}

			m.quitting = true
			printInfo("Switching to %s...", wt.Path)

			if err := os.Chdir(wt.Path); err != nil {
				m.quitting = false
				m.setErrorStatus(fmt.Sprintf("Failed to switch: %v", err), statusMessageTimeout)
				return m, tickCmd()
			}

			shell := getShell(m.config)
			cmd := exec.Command(shell)
			cmd.Stdin = os.Stdin
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Dir = wt.Path

			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				if err != nil {
					return err
				}
				return tea.Quit()
			})
		}
		return m, nil
	}
	return m, nil
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.ui.width = msg.Width
		m.ui.height = msg.Height
		return m, nil

	case worktreesLoadedMsg:
		m.wt.loading = false
		m.wt.loadingMsg = ""
		if msg.err != nil {
			m.wt.detailQueue = nil
			m.wt.activeDetailFetches = 0
			m.setErrorStatus(fmt.Sprintf("Load failed: %v", msg.err), statusMessageTimeout)
			return m, tickCmd()
		}
		previousStates := make(map[string]pullRequestState, len(m.wt.worktrees))
		for _, wt := range m.wt.worktrees {
			previousStates[wt.Branch] = wt.PRState
		}
		m.wt.worktrees = msg.worktrees
		for i := range m.wt.worktrees {
			if state, ok := previousStates[m.wt.worktrees[i].Branch]; ok {
				m.wt.worktrees[i].PRState = state
			}
		}
		m.wt.filtered = filterWorktrees(m.wt.worktrees, m.wt.searchTerm)
		m.wt.defaultBranch = msg.defaultBranch
		m.mainWorktreeDir = msg.mainWorktreeDir
		m.wt.detailGeneration++
		m.wt.prGeneration++
		m.wt.prFetchAttempts = 0
		m.resetDetailQueue()
		// Find current worktree path for tracking previous (session-only)
		if m.wt.previousWorktree == "" {
			for _, wt := range msg.worktrees {
				if wt.IsCurrent {
					m.wt.previousWorktree = wt.Path
					break
				}
			}
		}
		cmds := []tea.Cmd{tickCmd()}
		if detailCmd := m.startDetailFetches(); detailCmd != nil {
			cmds = append(cmds, detailCmd)
		}
		cmds = append(cmds, discoverPullRequestLookupsCmd(m.repoPath, m.wt.worktrees, m.wt.prGeneration))
		return m, tea.Batch(cmds...)

	case worktreeDetailLoadedMsg:
		if msg.generation != m.wt.detailGeneration {
			return m, tickCmd()
		}
		if m.wt.activeDetailFetches > 0 {
			m.wt.activeDetailFetches--
		}
		m.applyWorktreeDetail(msg.detail)
		if detailCmd := m.startDetailFetches(); detailCmd != nil {
			return m, tea.Batch(tickCmd(), detailCmd)
		}
		return m, tickCmd()

	case pullRequestLookupsLoadedMsg:
		if msg.generation != m.wt.prGeneration {
			return m, tickCmd()
		}
		if msg.err != nil {
			// PR status is optional. Leave all rows without a marker if the
			// local repository cannot be inspected for GitHub candidates.
			return m, tickCmd()
		}
		m.reconcilePullRequestStates(msg.lookups)
		if pullRequestCmd := m.startPullRequestFetch(); pullRequestCmd != nil {
			return m, tea.Batch(tickCmd(), pullRequestCmd)
		}
		return m, tickCmd()

	case pullRequestsLoadedMsg:
		if msg.generation != m.wt.prGeneration {
			return m, tickCmd()
		}
		m.applyPullRequestStates(msg.states)
		if msg.err != nil {
			if retryCmd := m.startPullRequestFetch(); retryCmd != nil {
				return m, tea.Batch(tickCmd(), retryCmd)
			}
			return m, tickCmd()
		}
		return m, tickCmd()

	case worktreeCreatedMsg:
		m.wt.loading = false
		m.wt.loadingMsg = ""
		if msg.err != nil {
			m.setErrorStatus(fmt.Sprintf("Create failed: %v", msg.err), statusMessageTimeout)
			return m, tickCmd()
		}
		m.setStatus(fmt.Sprintf("Created worktree: %s", msg.branch), statusMessageTimeout)
		refreshCmd := m.refreshWorktrees()
		return m, tea.Batch(tickCmd(), refreshCmd)

	case tickMsg:
		if !m.status.timeout.IsZero() && time.Now().After(m.status.timeout) {
			m.status.message = ""
			m.status.timeout = time.Time{}
		}
		return m, tickCmd()

	case tea.KeyMsg:
		// m.err is reserved for fatal errors; operational errors use setStatus

		if m.del.confirming {
			return m.handleDeleteConfirm(msg)
		}

		switch m.ui.inputMode {
		case modeSearch:
			return m.handleSearchMode(msg)
		case modeNewBranch:
			return m.handleNewBranchMode(msg)
		case modeActions:
			return m.handleActionsMode(msg)
		case modeMergeStrategy:
			return m.handleMergeStrategyMode(msg)
		case modeMergeConfirm:
			return m.handleMergeConfirmMode(msg)
		default:
			return m.handleNormalMode(msg)
		}
	}

	return m, nil
}

func getShell(config *Config) string {
	if config != nil && config.Shell != "" {
		return config.Shell
	}
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/bash"
}

func gitExcludePath(repoPath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("failed to resolve git dir: %w", err)
	}

	gitDir := strings.TrimSpace(string(output))
	if gitDir == "" {
		return "", fmt.Errorf("git dir not found")
	}

	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoPath, gitDir)
	}

	return filepath.Join(gitDir, "info", "exclude"), nil
}

func ensureGitExcludeEntry(repoPath, entry string) error {
	excludePath, err := gitExcludePath(repoPath)
	if err != nil {
		return err
	}

	// Ensure entry ends with / if it's a directory
	if !strings.HasSuffix(entry, "/") {
		entry = entry + "/"
	}

	// Read existing exclude file content
	content, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if entry already exists
	lines := strings.Split(string(content), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == entry || trimmed == strings.TrimRight(entry, "/") {
			// Entry already exists
			return nil
		}
	}

	// Add entry to git excludes
	newContent := string(content)
	if len(content) > 0 && !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}

	// Add a comment if this is the first worktree entry
	hasWorktreeComment := false
	for _, line := range lines {
		if strings.Contains(line, "Git worktrees") {
			hasWorktreeComment = true
			break
		}
	}

	if !hasWorktreeComment && len(content) > 0 {
		newContent += "\n# Git worktrees\n"
	} else if len(content) == 0 {
		newContent = "# Git worktrees\n"
	}

	newContent += entry + "\n"

	return os.WriteFile(excludePath, []byte(newContent), 0644)
}

func ensureNoTrackedFiles(repoPath, relPath string) error {
	relPath = strings.TrimSuffix(filepath.ToSlash(relPath), "/")
	if relPath == "" {
		return nil
	}

	cmd := exec.Command("git", "ls-files", "-z", "--", relPath)
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("failed to check tracked files under %s: %w", relPath, err)
	}
	if len(output) > 0 {
		return fmt.Errorf("worktree directory %q contains tracked files; use a different worktree_dir", relPath)
	}

	return nil
}

func refExists(repoPath, ref string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	_, err := runGitCmd(ctx, repoPath, "rev-parse", "--verify", ref)
	return err == nil
}

func localBranchExists(repoPath, branch string) bool {
	return refExists(repoPath, fmt.Sprintf("refs/heads/%s", branch))
}

type remoteBranchMatch struct {
	remote     string
	ref        string
	needsFetch bool
}

// getCheckoutDefaultRemote returns the value of the `checkout.defaultRemote`
// git config option, or "" if unset. A read error is treated as "unset" so
// callers fall back to the multi-remote ambiguity check.
func getCheckoutDefaultRemote(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	output, err := runGitCmd(ctx, repoPath, "config", "--get", "checkout.defaultRemote")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// listRemotes returns the configured remote names. A read error is treated
// as "no remotes" so callers skip remote discovery and create a fresh branch.
func listRemotes(repoPath string) []string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	output, err := runGitCmd(ctx, repoPath, "remote")
	if err != nil {
		return nil
	}
	return strings.Fields(string(output))
}

func listGitRemotesWithContext(ctx context.Context, repoPath string) ([]gitRemote, error) {
	output, err := runGitCmd(ctx, repoPath, "config", "--get-regexp", `^remote\..*\.url$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}

	var remotes []gitRemote
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := fields[0]
		if !strings.HasPrefix(key, "remote.") || !strings.HasSuffix(key, ".url") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		if name == "" {
			continue
		}
		remotes = append(remotes, gitRemote{
			Name: name,
			URL:  fields[1],
		})
	}
	return remotes, nil
}

type githubPullRequestNode struct {
	State               string `json:"state"`
	HeadRefOID          string `json:"headRefOid"`
	HeadRepositoryOwner struct {
		Login string `json:"login"`
	} `json:"headRepositoryOwner"`
}

type githubPullRequestConnection struct {
	Nodes []githubPullRequestNode `json:"nodes"`
}

type githubPullRequestRepository struct {
	PullRequests githubPullRequestConnection  `json:"pullRequests"`
	Parent       *githubPullRequestRepository `json:"parent"`
}

type githubPullRequestGraphQLResponse struct {
	Data   map[string]*githubPullRequestRepository `json:"data"`
	Errors []struct {
		Message string `json:"message"`
		Path    []any  `json:"path"`
	} `json:"errors"`
}

func githubPullRequestRepositoryAlias(index int) string {
	return fmt.Sprintf("repo%d", index)
}

func pullRequestNodeMatchesLookup(node githubPullRequestNode, lookup pullRequestLookup) bool {
	if lookup.Head != "" && !strings.EqualFold(node.HeadRefOID, lookup.Head) {
		return false
	}
	if lookup.HeadOwner == "" {
		return true
	}
	owner := strings.ToLower(strings.TrimSpace(node.HeadRepositoryOwner.Login))
	return owner != "" && owner == strings.ToLower(lookup.HeadOwner)
}

func mapPullRequestStates(lookups []pullRequestLookup, response githubPullRequestGraphQLResponse) map[string]pullRequestState {
	states := make(map[string]pullRequestState)
	for i, lookup := range lookups {
		if lookup.Branch == "" {
			continue
		}
		repo, ok := response.Data[githubPullRequestRepositoryAlias(i)]
		if !ok || repo == nil {
			continue
		}
		connections := []githubPullRequestConnection{repo.PullRequests}
		if repo.Parent != nil {
			connections = append([]githubPullRequestConnection{repo.Parent.PullRequests}, connections...)
		}
		for _, conn := range connections {
			for _, node := range conn.Nodes {
				if !pullRequestNodeMatchesLookup(node, lookup) {
					continue
				}
				if state := pullRequestStateFromGitHub(node.State); state != pullRequestStateNone {
					states[lookup.Branch] = state
					break
				}
			}
			if _, ok := states[lookup.Branch]; ok {
				break
			}
		}
	}
	return states
}

type branchUpstreamConfig struct {
	remote  string
	headRef string
}

func branchUpstreams(ctx context.Context, repoPath string) map[string]branchUpstreamConfig {
	output, err := runGitCmd(ctx, repoPath, "config", "--get-regexp", `^branch\..*\.(remote|merge)$`)
	if err != nil {
		return nil
	}

	upstreams := make(map[string]branchUpstreamConfig)
	for _, line := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key, value := fields[0], fields[1]
		if !strings.HasPrefix(key, "branch.") {
			continue
		}

		var branch string
		config := branchUpstreamConfig{}
		switch {
		case strings.HasSuffix(key, ".remote"):
			branch = strings.TrimSuffix(strings.TrimPrefix(key, "branch."), ".remote")
			config = upstreams[branch]
			config.remote = value
		case strings.HasSuffix(key, ".merge"):
			branch = strings.TrimSuffix(strings.TrimPrefix(key, "branch."), ".merge")
			config = upstreams[branch]
			config.headRef = strings.TrimPrefix(value, "refs/heads/")
		default:
			continue
		}
		if branch != "" {
			upstreams[branch] = config
		}
	}
	return upstreams
}

func githubRepoForRemote(remotes []gitRemote, remoteName string) (githubRepo, bool) {
	for _, remote := range remotes {
		if remote.Name == remoteName {
			return parseGitHubRemoteURL(remote.URL)
		}
	}
	return githubRepo{}, false
}

func githubReposForRemotes(remotes []gitRemote) []githubRepo {
	seen := make(map[githubRepo]bool)
	repos := make([]githubRepo, 0, len(remotes))
	for _, remote := range remotes {
		repo, ok := parseGitHubRemoteURL(remote.URL)
		if !ok || seen[repo] {
			continue
		}
		seen[repo] = true
		repos = append(repos, repo)
	}
	return repos
}

func uniquePullRequestLookups(ctx context.Context, repoPath string, worktrees []Worktree, remotes []gitRemote) []pullRequestLookup {
	seen := make(map[string]bool)
	unique := make([]pullRequestLookup, 0, len(worktrees))
	fallbackRepos := githubReposForRemotes(remotes)
	upstreams := branchUpstreams(ctx, repoPath)
	for _, wt := range worktrees {
		branch := strings.TrimSpace(wt.Branch)
		head := strings.TrimSpace(wt.Head)
		if branch == "" || head == "" {
			continue
		}
		if upstream := upstreams[branch]; upstream.remote != "" && upstream.headRef != "" {
			if upstreamRepo, ok := githubRepoForRemote(remotes, upstream.remote); ok {
				lookup := pullRequestLookup{
					Branch:    branch,
					HeadRef:   upstream.headRef,
					HeadOwner: strings.ToLower(upstreamRepo.Owner),
					HeadRepo:  upstreamRepo,
				}
				key := fmt.Sprintf("%s\x00%s\x00%s/%s", lookup.Branch, lookup.HeadRef, lookup.HeadRepo.Owner, lookup.HeadRepo.Name)
				if !seen[key] {
					seen[key] = true
					unique = append(unique, lookup)
				}
				continue
			}
		}
		for _, fallbackRepo := range fallbackRepos {
			lookup := pullRequestLookup{
				Branch:    branch,
				HeadRef:   branch,
				HeadOwner: strings.ToLower(fallbackRepo.Owner),
				HeadRepo:  fallbackRepo,
			}
			key := fmt.Sprintf("%s\x00%s\x00%s/%s", lookup.Branch, lookup.HeadRef, lookup.HeadRepo.Owner, lookup.HeadRepo.Name)
			if seen[key] {
				continue
			}
			seen[key] = true
			unique = append(unique, lookup)
		}
	}
	return unique
}

func buildPullRequestGraphQLQuery(lookups []pullRequestLookup) string {
	var b strings.Builder
	b.WriteString("query {\n")
	for i, lookup := range lookups {
		fmt.Fprintf(&b, "  %s: repository(owner: %s, name: %s) {\n", githubPullRequestRepositoryAlias(i), strconv.Quote(lookup.HeadRepo.Owner), strconv.Quote(lookup.HeadRepo.Name))
		fmt.Fprintf(&b,
			"    pullRequests(first: 10, headRefName: %s, states: [OPEN, CLOSED, MERGED], orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { state headRefOid headRepositoryOwner { login } } }\n",
			strconv.Quote(lookup.HeadRef),
		)
		fmt.Fprintf(&b,
			"    parent { pullRequests(first: 10, headRefName: %s, states: [OPEN, CLOSED, MERGED], orderBy: {field: UPDATED_AT, direction: DESC}) { nodes { state headRefOid headRepositoryOwner { login } } } }\n",
			strconv.Quote(lookup.HeadRef),
		)
		b.WriteString("  }\n")
	}
	b.WriteString("}\n")
	return b.String()
}

func fetchPullRequestStates(ctx context.Context, client *http.Client, endpoint, token string, lookups []pullRequestLookup) (map[string]pullRequestState, error) {
	if client == nil {
		client = http.DefaultClient
	}
	resolvedStates := make(map[string]pullRequestState)
	if len(lookups) == 0 {
		return resolvedStates, nil
	}
	successful := make([]bool, len(lookups))
	matchedStates := make(map[string]pullRequestState)
	var fetchErrors []error

	for start := 0; start < len(lookups); start += githubPRBatchSize {
		end := start + githubPRBatchSize
		if end > len(lookups) {
			end = len(lookups)
		}
		batch := lookups[start:end]
		body, err := json.Marshal(map[string]string{
			"query": buildPullRequestGraphQLQuery(batch),
		})
		if err != nil {
			fetchErrors = append(fetchErrors, err)
			continue
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			fetchErrors = append(fetchErrors, err)
			continue
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			fetchErrors = append(fetchErrors, err)
			continue
		}
		func() {
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				err = fmt.Errorf("github graphql returned status %d", resp.StatusCode)
				fetchErrors = append(fetchErrors, err)
				return
			}

			var graphQLResp githubPullRequestGraphQLResponse
			if decodeErr := json.NewDecoder(resp.Body).Decode(&graphQLResp); decodeErr != nil {
				err = decodeErr
				fetchErrors = append(fetchErrors, err)
				return
			}

			for i := range batch {
				alias := githubPullRequestRepositoryAlias(i)
				if graphQLResp.Data[alias] != nil {
					successful[start+i] = true
				}
			}
			failedAliases := make(map[string]bool)
			unscopedError := false
			for _, graphQLError := range graphQLResp.Errors {
				if len(graphQLError.Path) == 0 {
					unscopedError = true
					continue
				}
				if alias, ok := graphQLError.Path[0].(string); ok {
					failedAliases[alias] = true
				} else {
					unscopedError = true
				}
			}
			if unscopedError {
				for i := range batch {
					failedAliases[githubPullRequestRepositoryAlias(i)] = true
				}
			}
			for i := range batch {
				if failedAliases[githubPullRequestRepositoryAlias(i)] {
					successful[start+i] = false
				}
			}
			for branch, state := range mapPullRequestStates(batch, graphQLResp) {
				matchedStates[branch] = state
			}
			for _, graphQLError := range graphQLResp.Errors {
				fetchErrors = append(fetchErrors, fmt.Errorf("github graphql error: %s", graphQLError.Message))
			}
			for i := range batch {
				if !successful[start+i] {
					fetchErrors = append(fetchErrors, fmt.Errorf("github graphql did not resolve %s", githubPullRequestRepositoryAlias(i)))
				}
			}
		}()
	}

	for i, lookup := range lookups {
		if lookup.Branch == "" {
			continue
		}
		if state, ok := matchedStates[lookup.Branch]; ok {
			resolvedStates[lookup.Branch] = state
			continue
		}
		if !successful[i] {
			continue
		}
		allSuccessful := true
		for j, otherLookup := range lookups {
			if otherLookup.Branch == lookup.Branch && !successful[j] {
				allSuccessful = false
				break
			}
		}
		if allSuccessful {
			resolvedStates[lookup.Branch] = pullRequestStateNone
		}
	}

	return resolvedStates, errors.Join(fetchErrors...)
}

func loadPullRequestLookups(ctx context.Context, repoPath string, worktrees []Worktree) ([]pullRequestLookup, error) {
	remotes, err := listGitRemotesWithContext(ctx, repoPath)
	if err != nil {
		return nil, err
	}
	return uniquePullRequestLookups(ctx, repoPath, worktrees, remotes), nil
}

func loadPullRequestStatesForLookups(ctx context.Context, lookups []pullRequestLookup, tokenLookup tokenLookupFunc, client *http.Client) (map[string]pullRequestState, error) {
	if len(lookups) == 0 {
		return map[string]pullRequestState{}, nil
	}
	if tokenLookup == nil {
		tokenLookup = defaultTokenLookup
	}
	token, _ := tokenLookup("github.com")
	if token == "" {
		return nil, errors.New("github token not found")
	}

	return fetchPullRequestStates(ctx, client, githubGraphQLEndpoint, token, lookups)
}

func loadPullRequestStates(ctx context.Context, repoPath string, worktrees []Worktree, tokenLookup tokenLookupFunc, client *http.Client) (map[string]pullRequestState, error) {
	lookups, err := loadPullRequestLookups(ctx, repoPath, worktrees)
	if err != nil {
		return nil, err
	}
	return loadPullRequestStatesForLookups(ctx, lookups, tokenLookup, client)
}

func findRemoteBranch(repoPath, remote, branch string) (*remoteBranchMatch, bool) {
	if refExists(repoPath, fmt.Sprintf("refs/remotes/%s/%s", remote, branch)) {
		return &remoteBranchMatch{
			remote: remote,
			ref:    fmt.Sprintf("%s/%s", remote, branch),
		}, true
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitCmdSlowTimeout)
	defer cancel()
	output, err := runGitCmd(ctx, repoPath, "ls-remote", "--heads", remote, fmt.Sprintf("refs/heads/%s", branch))
	if err == nil && len(strings.TrimSpace(string(output))) > 0 {
		return &remoteBranchMatch{
			remote:     remote,
			ref:        fmt.Sprintf("%s/%s", remote, branch),
			needsFetch: true,
		}, true
	}

	return nil, false
}

func fetchRemoteBranch(repoPath, remote, branch string) error {
	refspec := fmt.Sprintf("+refs/heads/%s:refs/remotes/%s/%s", branch, remote, branch)
	output, err := runGitCmdCombinedWithTimeout(repoPath, worktreeCmdTimeout, "fetch", remote, refspec)
	if err != nil {
		return fmt.Errorf("failed to fetch %s/%s: %s", remote, branch, strings.TrimSpace(string(output)))
	}
	return nil
}

// resolveRemoteBranchForSwitch implements the same remote-guessing behavior as
// `git switch <name>` (a.k.a. --guess-remote): when `checkout.defaultRemote` is
// set and the branch exists on that remote, that one wins; otherwise the branch
// must exist on exactly one remote. Anything ambiguous returns an error.
func resolveRemoteBranchForSwitch(repoPath, branch string) (*remoteBranchMatch, error) {
	remotes := listRemotes(repoPath)
	if len(remotes) == 0 {
		return nil, nil
	}

	defaultRemote := getCheckoutDefaultRemote(repoPath)
	if defaultRemote != "" {
		for _, remote := range remotes {
			if remote == defaultRemote {
				match, ok := findRemoteBranch(repoPath, remote, branch)
				if ok {
					return match, nil
				}
			}
		}
	}

	var matches []remoteBranchMatch
	for _, remote := range remotes {
		if match, ok := findRemoteBranch(repoPath, remote, branch); ok {
			matches = append(matches, *match)
		}
	}

	if len(matches) == 0 {
		return nil, nil
	}
	if len(matches) > 1 {
		remoteNames := make([]string, 0, len(matches))
		for _, match := range matches {
			remoteNames = append(remoteNames, match.remote)
		}
		return nil, fmt.Errorf(
			"branch %q exists on multiple remotes (%s); set checkout.defaultRemote or pass an explicit source branch",
			branch,
			strings.Join(remoteNames, ", "),
		)
	}

	return &matches[0], nil
}

func createWorktreeFromBranch(repoPath, worktreeName, sourceBranch string, config *Config) (string, error) {
	if err := validateBranchName(worktreeName); err != nil {
		return "", fmt.Errorf("invalid branch name: %w", err)
	}

	worktreeDir, err := ensureWorktreeDir(repoPath, config)
	if err != nil {
		return "", err
	}

	// Generate worktree path
	worktreePath := filepath.Join(worktreeDir, strings.ReplaceAll(worktreeName, "/", "-"))

	// Create worktree with new branch from source branch
	args := []string{"worktree", "add", "-b", worktreeName, worktreePath, sourceBranch}
	_, err = runGitCmdCombinedWithTimeout(repoPath, worktreeCmdTimeout, args...)
	if err != nil {
		// If branch already exists, try without -b flag
		args = []string{"worktree", "add", worktreePath, worktreeName}
		output, err := runGitCmdCombinedWithTimeout(repoPath, worktreeCmdTimeout, args...)
		if err != nil {
			return "", fmt.Errorf("failed to create worktree: %s", strings.TrimSpace(string(output)))
		}
	}

	return worktreePath, nil
}

func createWorktree(repoPath, branch string, config *Config) (string, error) {
	if err := validateBranchName(branch); err != nil {
		return "", fmt.Errorf("invalid branch name: %w", err)
	}

	worktreeDir, err := ensureWorktreeDir(repoPath, config)
	if err != nil {
		return "", err
	}

	// Generate worktree path
	worktreePath := filepath.Join(worktreeDir, strings.ReplaceAll(branch, "/", "-"))

	// Determine best strategy based on existing refs
	var (
		args        []string
		localExists = localBranchExists(repoPath, branch)
		remoteMatch *remoteBranchMatch
	)

	switch {
	case localExists:
		args = []string{"worktree", "add", worktreePath, branch}
	default:
		match, err := resolveRemoteBranchForSwitch(repoPath, branch)
		if err != nil {
			return "", err
		}
		remoteMatch = match
		if remoteMatch != nil {
			if remoteMatch.needsFetch {
				if err := fetchRemoteBranch(repoPath, remoteMatch.remote, branch); err != nil {
					return "", err
				}
			}
			fullRef := fmt.Sprintf("refs/remotes/%s/%s", remoteMatch.remote, branch)
			if !refExists(repoPath, fullRef) {
				return "", fmt.Errorf("failed to resolve remote branch %s", remoteMatch.ref)
			}
			// Use --no-track and set upstream explicitly afterwards. `--track`
			// rejects start points that aren't covered by the remote's
			// configured fetch refspec (e.g. single-branch clones), which would
			// fail even though we just fetched the ref.
			args = []string{"worktree", "add", "--no-track", "-b", branch, worktreePath, fullRef}
		} else {
			args = []string{"worktree", "add", "-b", branch, worktreePath}
		}
	}

	output, err := runGitCmdCombinedWithTimeout(repoPath, worktreeCmdTimeout, args...)
	if err != nil {
		return "", fmt.Errorf("failed to create worktree: %s", strings.TrimSpace(string(output)))
	}

	// Point the new branch's upstream at the remote branch we used. Done by
	// writing config directly (not via `branch --set-upstream-to`) so it works
	// in single-branch / restricted-refspec clones, where git refuses to track
	// a ref outside the configured fetch refspec.
	if remoteMatch != nil {
		remoteKey := fmt.Sprintf("branch.%s.remote", branch)
		mergeKey := fmt.Sprintf("branch.%s.merge", branch)
		mergeRef := fmt.Sprintf("refs/heads/%s", branch)
		if out, err := runGitCmdCombinedWithTimeout(repoPath, gitCmdTimeout, "config", remoteKey, remoteMatch.remote); err != nil {
			return "", fmt.Errorf("failed to set upstream for %s: %s", branch, strings.TrimSpace(string(out)))
		}
		if out, err := runGitCmdCombinedWithTimeout(repoPath, gitCmdTimeout, "config", mergeKey, mergeRef); err != nil {
			return "", fmt.Errorf("failed to set upstream for %s: %s", branch, strings.TrimSpace(string(out)))
		}
	}

	return worktreePath, nil
}

func deleteWorktree(repoPath, worktreePath string) error {
	cmd := exec.Command("git", "worktree", "remove", worktreePath, "--force")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to remove worktree: %s", string(output))
	}
	return nil
}

// canFastForward checks if sourceBranch can be fast-forwarded into targetBranch.
// Returns true if targetBranch is an ancestor of sourceBranch.
func canFastForward(repoPath, sourceBranch, targetBranch string) (bool, error) {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", targetBranch, sourceBranch)
	cmd.Dir = repoPath
	err := cmd.Run()
	if err != nil {
		// Exit code 1 means not an ancestor (not a fast-forward)
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// canMergeWorktree checks if a worktree can be merged into the default branch.
// Returns detailed status for user feedback.
func canMergeWorktree(repoPath string, wt *Worktree, defaultBranch string) (canFF bool, ahead, behind int, hasChanges bool, err error) {
	// Check for uncommitted changes
	cmd := exec.Command("git", "status", "--porcelain")
	cmd.Dir = wt.Path
	output, err := cmd.Output()
	if err != nil {
		return false, 0, 0, false, fmt.Errorf("failed to check status: %w", err)
	}
	hasChanges = len(strings.TrimSpace(string(output))) > 0

	// Get ahead/behind counts
	ahead, behind = getAheadBehind(wt.Path, wt.Branch, defaultBranch)

	// Check if fast-forward is possible
	canFF, err = canFastForward(repoPath, wt.Branch, defaultBranch)
	if err != nil {
		return false, ahead, behind, hasChanges, fmt.Errorf("failed to check fast-forward: %w", err)
	}

	return canFF, ahead, behind, hasChanges, nil
}

// mergeWorktreeBranch performs the merge operation.
func mergeWorktreeBranch(repoPath, branch, defaultBranch, strategy string) error {
	// Checkout the default branch
	cmd := exec.Command("git", "checkout", defaultBranch)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to checkout %s: %s", defaultBranch, string(output))
	}

	// Perform the merge based on strategy
	switch strategy {
	case "squash":
		cmd = exec.Command("git", "merge", "--squash", branch)
		cmd.Dir = repoPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to squash merge: %s", string(output))
		}
		// Commit the squashed changes
		commitMsg := fmt.Sprintf("Squash merge branch '%s'", branch)
		cmd = exec.Command("git", "commit", "-m", commitMsg)
		cmd.Dir = repoPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to commit squash merge: %s", string(output))
		}
	case "ff-only":
		cmd = exec.Command("git", "merge", "--ff-only", branch)
		cmd.Dir = repoPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to fast-forward merge: %s", string(output))
		}
	default:
		return fmt.Errorf("unknown merge strategy: %s", strategy)
	}

	return nil
}

// deleteBranchAfterMerge deletes the branch after a successful merge.
func deleteBranchAfterMerge(repoPath, branch string, wasSquash bool) error {
	// Use -D for squash (no merge commit means -d would fail)
	// Use -d for ff-only (safe delete)
	flag := "-d"
	if wasSquash {
		flag = "-D"
	}
	cmd := exec.Command("git", "branch", flag, branch)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to delete branch: %s", string(output))
	}
	return nil
}

// mergeAndDeleteWorktree orchestrates the full merge and delete workflow.
func mergeAndDeleteWorktree(repoPath string, wt *Worktree, defaultBranch, strategy string) error {
	// Pre-flight checks
	canFF, ahead, behind, hasChanges, err := canMergeWorktree(repoPath, wt, defaultBranch)
	if err != nil {
		return err
	}

	if hasChanges {
		return errors.New("worktree has uncommitted changes - commit or stash first")
	}

	if wt.Branch == defaultBranch {
		return errors.New("cannot merge default branch into itself")
	}

	if ahead == 0 {
		return errors.New("branch has no new commits to merge")
	}

	if strategy == "ff-only" && !canFF {
		if behind > 0 {
			return fmt.Errorf("branch is behind %s by %d commits - rebase first", defaultBranch, behind)
		}
		return errors.New("fast-forward not possible - branches have diverged")
	}

	// Step 1: Remove the worktree (so branch is not checked out)
	if err := deleteWorktree(repoPath, wt.Path); err != nil {
		return fmt.Errorf("failed to remove worktree: %w", err)
	}

	// Step 2: Perform the merge
	if err := mergeWorktreeBranch(repoPath, wt.Branch, defaultBranch, strategy); err != nil {
		return fmt.Errorf("merge failed: %w", err)
	}

	// Step 3: Delete the branch
	if err := deleteBranchAfterMerge(repoPath, wt.Branch, strategy == "squash"); err != nil {
		// Don't fail completely - worktree is already deleted and merge succeeded
		return fmt.Errorf("merged successfully but failed to delete branch: %w", err)
	}

	return nil
}

func (m model) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err))
	}

	var s strings.Builder

	// Title
	title := fmt.Sprintf("Git Worktrees - %s", m.repoPath)
	s.WriteString(titleStyle.Render(title) + "\n")

	// Loading indicator
	if m.wt.loading {
		s.WriteString("\n" + dimStyle.Render(m.wt.loadingMsg) + "\n")
		return s.String()
	}

	// Search or input
	switch m.ui.inputMode {
	case modeSearch:
		s.WriteString(searchStyle.Render("Search: ") + m.wt.searchTerm + "█\n\n")
	case modeNewBranch:
		s.WriteString(searchStyle.Render("New branch name: ") + m.ui.inputValue + "█\n\n")
	case modeActions:
		if m.ui.cursor < len(m.wt.filtered) {
			wt := m.wt.filtered[m.ui.cursor]
			s.WriteString(searchStyle.Render(fmt.Sprintf("Actions for %s:\n", wt.Branch)))
			for i, action := range m.acts.actions {
				cursor := "  "
				if i == m.acts.cursor {
					cursor = "▸ "
				}
				line := fmt.Sprintf("%s[%s] %s", cursor, action.key, action.label)
				if i == m.acts.cursor {
					s.WriteString(selectedStyle.Render(line) + "\n")
				} else {
					s.WriteString(line + "\n")
				}
			}
			s.WriteString(helpStyle.Render("\n[enter] select  [esc] cancel\n"))
		}
		return s.String()
	case modeMergeStrategy:
		if m.merge.target != nil {
			s.WriteString(searchStyle.Render(fmt.Sprintf("Merge '%s' into '%s':\n\n", m.merge.target.Branch, m.wt.defaultBranch)))
			s.WriteString("  [s] Squash      - combine all commits into one\n")
			s.WriteString("  [f] Fast-forward - keep commit history\n")
			s.WriteString(helpStyle.Render("\n[esc] cancel\n"))
		}
		return s.String()
	case modeMergeConfirm:
		if m.merge.target != nil {
			strategyLabel := "fast-forward"
			if m.merge.strategy == "squash" {
				strategyLabel = "squash"
			}
			s.WriteString(errorStyle.Render(fmt.Sprintf("\nMerge '%s' into '%s' using %s and delete? [y/N] ",
				m.merge.target.Branch, m.wt.defaultBranch, strategyLabel)))
		}
		return s.String()
	default:
		if m.wt.searchTerm != "" {
			s.WriteString(searchStyle.Render("Search: ") + dimStyle.Render(m.wt.searchTerm) + "\n\n")
		} else {
			s.WriteString("\n")
		}
	}

	// Confirmation dialog
	if m.del.confirming && m.del.target != nil {
		s.WriteString(errorStyle.Render(fmt.Sprintf("\nDelete worktree '%s'? [y/N] ", m.del.target.Branch)))
		return s.String()
	}

	// Worktree list
	if len(m.wt.filtered) == 0 {
		s.WriteString(dimStyle.Render("  No worktrees found\n"))
	} else {
		viewportHeight := m.ui.height - viewportPadding
		if viewportHeight < minViewportHeight {
			viewportHeight = minViewportHeight
		}

		for i := m.ui.scrollOffset; i < len(m.wt.filtered) && i < m.ui.scrollOffset+viewportHeight; i++ {
			wt := m.wt.filtered[i]
			// Cursor indicator
			cursor := "  "
			if i == m.ui.cursor {
				cursor = "▸ "
			}
			// Folder mismatch — shown when folder name doesn't match branch
			isMainWorktree := filepath.Clean(wt.Path) == filepath.Clean(m.mainWorktreeDir)
			hasFolderMismatch := !isMainWorktree && !isExpectedWorktreePath(wt.Branch, wt.Path)
			folderName := ""
			if hasFolderMismatch {
				folderName = filepath.Base(filepath.Clean(wt.Path))
			}

			// Status indicator
			status := "✓"
			if wt.IsDirty {
				status = dirtyStyle.Render("●")
			}

			// Ahead/behind indicator
			var aheadBehind string
			if wt.Ahead > 0 || wt.Behind > 0 {
				var parts []string
				if wt.Ahead > 0 {
					parts = append(parts, aheadStyle.Render(fmt.Sprintf("↑%d", wt.Ahead)))
				}
				if wt.Behind > 0 {
					parts = append(parts, behindStyle.Render(fmt.Sprintf("↓%d", wt.Behind)))
				}
				aheadBehind = " " + strings.Join(parts, " ")
			}

			statusAndPR := renderWorktreeStatus(status, wt.PRState)
			// Branch name
			branchText := wt.Branch
			if branchText == "" {
				branchText = "(detached)"
			}
			if wt.IsCurrent {
				branchText = "● " + branchText
			}

			shortFolderLabel := ""
			branchSuffix := " " + statusAndPR + aheadBehind + " "
			if hasFolderMismatch {
				shortFolderLabel = " " + dimStyle.Render("[📂]")
				branchSuffix = shortFolderLabel + " │ " + statusAndPR + aheadBehind + " "
			}

			branchColumnWidth := max(branchDisplayWidth, lipgloss.Width(branchText))
			if m.ui.width > 0 {
				// Reserve the cursor, folder cue, and status symbols before sizing the
				// branch column. These cues stay visible even for long branch names.
				available := m.ui.width - lipgloss.Width(cursor) - lipgloss.Width(branchSuffix)
				branchColumnWidth = max(1, min(branchColumnWidth, available))
			}

			branchText = truncateToWidth(branchText, branchColumnWidth)
			branch := branchStyle.Render(branchText)
			if wt.IsCurrent {
				branch = currentStyle.Render(branchText)
			}
			branch += strings.Repeat(" ", max(0, branchColumnWidth-lipgloss.Width(branchText)))

			// Commit info
			commitMsg := truncateString(wt.LastCommit.Message, maxCommitMsgLength)
			relTime := formatRelativeTime(wt.LastCommit.Date)
			commitText := fmt.Sprintf("%s (%s)", commitMsg, relTime)

			// Build folder label: "[📂 name]" if it fits, just "[📂]" if tight
			folderLabel := ""
			if hasFolderMismatch {
				folderLabel = shortFolderLabel
				basePrefix := cursor + branch
				tail := " │ " + statusAndPR + aheadBehind + " "
				fullLabel := " " + dimStyle.Render("[📂 "+folderName+"]")
				commitWidth := lipgloss.Width(commitText)
				if m.ui.width <= 0 || lipgloss.Width(basePrefix)+lipgloss.Width(fullLabel)+lipgloss.Width(tail)+commitWidth <= m.ui.width {
					folderLabel = fullLabel
				}
			}

			// Format line: cursor branch [📂 name] │ status PR ahead/behind  commit
			var separator string
			if folderLabel != "" {
				separator = " │ "
			} else {
				separator = " "
			}
			prefix := cursor + branch + folderLabel + separator + statusAndPR + aheadBehind + " "
			maxDetailWidth := 0
			if m.ui.width > 0 {
				maxDetailWidth = m.ui.width - lipgloss.Width(prefix)
			}
			detailText := truncateToWidth(commitText, maxDetailWidth)

			line := fmt.Sprintf("%s%s",
				prefix,
				dimStyle.Render(detailText),
			)

			if i == m.ui.cursor {
				s.WriteString(selectedStyle.Render(line) + "\n")
			} else {
				s.WriteString(line + "\n")
			}
		}
	}

	// Error message (operational errors, not fatal)
	if m.err != nil {
		s.WriteString("\n" + errorStyle.Render(fmt.Sprintf("Error: %v", m.err)) + "\n")
	}

	// Status message
	if m.status.message != "" {
		style := successStyle
		if m.status.isError {
			style = errorStyle
		}
		s.WriteString("\n" + style.Render(m.status.message) + "\n")
	}

	// Help
	s.WriteString("\n")
	if m.ui.inputMode == modeNormal {
		help := "[n]ew  [d]elete  [m]erge  [a]ctions  [enter] switch  [/] search  [r]efresh  [^] default  [-] prev  [@] current  [q]uit"
		s.WriteString(helpStyle.Render(help))
	} else {
		help := "[enter] confirm  [esc] cancel"
		s.WriteString(helpStyle.Render(help))
	}

	return s.String()
}

func generateBashCompletion() string {
	return `_gt_completions() {
    local cur="${COMP_WORDS[COMP_CWORD]}"
    local prev="${COMP_WORDS[COMP_CWORD-1]}"

    # Get list of branches for completion
    if [[ "$prev" == "gt" ]] || [[ "$prev" == "-x" ]] || [[ "$prev" == "--execute" ]]; then
        local branches=$(git branch --format='%(refname:short)' 2>/dev/null)
        local worktrees=$(git worktree list --porcelain 2>/dev/null | grep "^branch" | sed 's/branch refs\/heads\///')
        COMPREPLY=($(compgen -W "$branches $worktrees -h --help -v --version -x --execute" -- "$cur"))
    elif [[ "$cur" == -* ]]; then
        COMPREPLY=($(compgen -W "-h --help -v --version -x --execute" -- "$cur"))
    else
        local branches=$(git branch --format='%(refname:short)' 2>/dev/null)
        COMPREPLY=($(compgen -W "$branches" -- "$cur"))
    fi
}

complete -F _gt_completions gt
`
}

func generateZshCompletion() string {
	return `#compdef gt

_gt() {
    local -a branches worktrees

    # Get branches
    branches=(${(f)"$(git branch --format='%(refname:short)' 2>/dev/null)"})

    # Get worktree branches
    worktrees=(${(f)"$(git worktree list --porcelain 2>/dev/null | grep '^branch' | sed 's/branch refs\/heads\///')"})

    _arguments \
        '(-h --help)'{-h,--help}'[Show help message]' \
        '(-v --version)'{-v,--version}'[Show version]' \
        '(-x --execute)'{-x,--execute}'[Execute command after switch]:command:_command_names' \
        '1:worktree name:->worktree' \
        '2:source branch:->branch'

    case $state in
        worktree|branch)
            _describe -t branches 'branches' branches
            _describe -t worktrees 'worktrees' worktrees
            ;;
    esac
}

_gt "$@"
`
}

func generateFishCompletion() string {
	return `# Fish completion for gt

function __gt_branches
    git branch --format='%(refname:short)' 2>/dev/null
end

function __gt_worktrees
    git worktree list --porcelain 2>/dev/null | string match -r '^branch refs/heads/(.*)' | string replace 'branch refs/heads/' ''
end

# Disable file completion
complete -c gt -f

# Options
complete -c gt -s h -l help -d 'Show help message'
complete -c gt -s v -l version -d 'Show version'
complete -c gt -s x -l execute -d 'Execute command after switch' -r

# Subcommands
complete -c gt -n '__fish_is_first_arg' -a '(__gt_branches)' -d 'Branch'
complete -c gt -n '__fish_is_first_arg' -a '(__gt_worktrees)' -d 'Worktree'
complete -c gt -n 'not __fish_is_first_arg' -a '(__gt_branches)' -d 'Source branch'
`
}

func printCompletion(shell string) {
	switch shell {
	case "bash":
		fmt.Print(generateBashCompletion())
	case "zsh":
		fmt.Print(generateZshCompletion())
	case "fish":
		fmt.Print(generateFishCompletion())
	default:
		fmt.Fprintf(os.Stderr, "Unknown shell: %s\nSupported shells: bash, zsh, fish\n", shell)
		os.Exit(1)
	}
}

func printHelp() {
	help := fmt.Sprintf(`gt - Git Worktree Manager v%s

USAGE:
    gt                           Open interactive worktree manager
    gt <name>                    Create or reuse branch, then switch to its worktree
    gt <name> <branch>           Create worktree from specified branch and switch to it
    gt <name> -x <cmd>           Create worktree and execute command instead of shell
    gt --merge <branch>          Merge worktree branch into default and delete
    gt completion <shell>        Generate shell completion script (bash, zsh, fish)
    gt -h, --help                Show this help message
    gt -v, --version             Show version information

OPTIONS:
    -x, --execute <cmd>          Execute command after switching (instead of shell)
    --merge <branch>             Merge worktree's branch into default branch and delete
    --squash                     Use squash merge (with --merge)
    --ff-only                    Use fast-forward only merge (with --merge, default)

EXAMPLES:
    gt                           # Open TUI to manage worktrees
    gt feature-xyz               # Reuse local/remote branch, or create from current branch
    gt fix-bug main              # Create worktree 'fix-bug' from 'main' branch
    gt feature-xyz -x "code ."   # Create worktree and open in VS Code
    gt feature-xyz -x claude     # Create worktree and start Claude Code
    gt --merge feature-xyz       # Merge 'feature-xyz' into main (ff-only)
    gt --merge feature-xyz --squash  # Squash merge into main

BRANCH CREATION:
    gt <name> reuses an existing local branch when one exists. If no local branch
    exists, gt follows git switch's remote guess behavior: it uses a matching
    remote-tracking branch, or fetches a matching branch discovered on exactly
    one remote or checkout.defaultRemote. If no local or remote branch matches,
    gt creates a new branch from the current HEAD. gt <name> <branch> always
    creates <name> from <branch>.

SHELL COMPLETION:
    # Bash: Add to ~/.bashrc
    eval "$(gt completion bash)"

    # Zsh: Add to ~/.zshrc
    eval "$(gt completion zsh)"

    # Fish: Add to ~/.config/fish/config.fish
    gt completion fish | source

INTERACTIVE MODE COMMANDS:
    n        Create new worktree
    d        Delete selected worktree
    m        Merge worktree into default branch and delete
    a        Open actions menu for selected worktree
    enter    Switch to selected worktree
    /        Search worktrees
    r        Refresh list
    ^        Jump to default branch worktree
    -        Jump to previous worktree
    @        Jump to current worktree
    q        Quit

VISUAL STATUS:
    ?        GitHub PR status is loading or unavailable
    ⎇        Lazygit-style GitHub PR status indicator (open, closed, or merged)

QUICK ACTIONS (from actions menu):
    e        Open in editor ($EDITOR)
    c        Open in VS Code
    t        Open terminal here
    f        Open in Finder
    p        Copy path to clipboard

CONFIGURATION:
    Config file: ~/.config/gt/config.json

    Options:
    - worktree_dir: Directory for worktrees (default: .worktrees)
                    Relative paths use the main worktree in normal repos
                    Falls back to the current Git top-level otherwise
                    Use absolute path to place worktrees outside repo
                    Use "../" prefix to place worktrees as siblings
    - shell: Shell to use when switching (default: $SHELL or /bin/bash)
    - post_create: Command to run after creating a worktree (e.g., "npm install")
    - default_merge_strategy: Default merge strategy - "squash" or "ff-only" (default)

    Example configs:

    # Default (inside repo):
    { "worktree_dir": ".worktrees" }

    # Sibling directories (like worktrunk):
    { "worktree_dir": "../gt-worktrees" }

    # Absolute path:
    { "worktree_dir": "/Users/me/worktrees/gt" }

    # With post-create hook:
    {
      "worktree_dir": "../gt-worktrees",
      "post_create": "npm install"
    }
`, version)
	fmt.Print(help)
}

func getDefaultBranchWithContext(ctx context.Context, repoPath string) string {
	// Try to get the default branch from remote
	output, err := runGitCmd(ctx, repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil {
		ref := strings.TrimSpace(string(output))
		// refs/remotes/origin/main -> main
		parts := strings.Split(ref, "/")
		if len(parts) > 0 {
			return parts[len(parts)-1]
		}
	}

	// Fallback: check common default branch names
	for _, branch := range []string{"main", "master"} {
		_, err := runGitCmd(ctx, repoPath, "rev-parse", "--verify", branch)
		if err == nil {
			return branch
		}
	}

	return "main"
}

func getDefaultBranch(repoPath string) string {
	ctx, cancel := context.WithTimeout(context.Background(), gitCmdTimeout)
	defer cancel()
	return getDefaultBranchWithContext(ctx, repoPath)
}

func runPostCreateHook(worktreePath string, config *Config) error {
	if config == nil || config.PostCreate == "" {
		return nil
	}

	fmt.Printf("\033[2mRunning post-create hook: %s\033[0m\n", config.PostCreate)

	shell := getShell(config)
	cmd := exec.Command(shell, "-c", config.PostCreate)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = worktreePath

	return cmd.Run()
}

func switchToWorktree(worktreePath string, config *Config, executeCmd string) error {
	// Change to the worktree directory
	if err := os.Chdir(worktreePath); err != nil {
		return err
	}

	fmt.Printf("\033[2mSwitched to %s\033[0m\n", worktreePath)

	// If an execute command is specified, run it instead of starting a shell
	if executeCmd != "" {
		shell := getShell(config)
		cmd := exec.Command(shell, "-c", executeCmd)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Dir = worktreePath
		return cmd.Run()
	}

	// Start a new shell in the worktree directory
	shell := getShell(config)
	cmd := exec.Command(shell)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = worktreePath

	return cmd.Run()
}

func parseArgs() (worktreeName, sourceBranch, executeCmd, completionShell string, showHelp, showVersion, mergeMode bool, mergeStrategy string) {
	args := os.Args[1:]
	var positional []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "-h" || arg == "--help" || arg == "help":
			showHelp = true
			return
		case arg == "-v" || arg == "--version" || arg == "version":
			showVersion = true
			return
		case arg == "completion":
			if i+1 < len(args) {
				i++
				completionShell = args[i]
			} else {
				completionShell = "bash"
			}
			return
		case arg == "-x" || arg == "--execute":
			if i+1 < len(args) {
				i++
				executeCmd = args[i]
			}
		case strings.HasPrefix(arg, "-x="):
			executeCmd = strings.TrimPrefix(arg, "-x=")
		case strings.HasPrefix(arg, "--execute="):
			executeCmd = strings.TrimPrefix(arg, "--execute=")
		case arg == "--merge":
			mergeMode = true
		case arg == "--squash":
			mergeStrategy = "squash"
		case arg == "--ff-only":
			mergeStrategy = "ff-only"
		case !strings.HasPrefix(arg, "-"):
			positional = append(positional, arg)
		}
	}

	if len(positional) > 0 {
		worktreeName = positional[0]
	}
	if len(positional) > 1 {
		sourceBranch = positional[1]
	}

	// Default merge strategy if not specified
	if mergeMode && mergeStrategy == "" {
		mergeStrategy = "ff-only"
	}

	return
}

func main() {
	worktreeName, sourceBranch, executeCmd, completionShell, showHelp, showVersion, mergeMode, mergeStrategy := parseArgs()

	if showHelp {
		printHelp()
		os.Exit(0)
	}

	if showVersion {
		fmt.Printf("gt version %s\n", version)
		os.Exit(0)
	}

	if completionShell != "" {
		printCompletion(completionShell)
		os.Exit(0)
	}

	// Handle merge mode
	if mergeMode {
		if worktreeName == "" {
			fmt.Fprintf(os.Stderr, "Error: --merge requires a worktree/branch name\n")
			os.Exit(1)
		}

		repoPath, err := getCurrentRepoPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		config, _ := loadConfig()
		if config == nil {
			config = &Config{}
		}

		// Get worktrees and find the target
		worktrees, err := getWorktrees(repoPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		var target *Worktree
		for i := range worktrees {
			if worktrees[i].Branch == worktreeName {
				target = &worktrees[i]
				break
			}
		}

		if target == nil {
			fmt.Fprintf(os.Stderr, "Error: worktree with branch '%s' not found\n", worktreeName)
			os.Exit(1)
		}

		// Get default branch
		defaultBranch := getDefaultBranch(repoPath)

		// Validate
		if target.IsCurrent {
			fmt.Fprintf(os.Stderr, "Error: cannot merge currently checked out worktree\n")
			os.Exit(1)
		}

		// Use config default if strategy not specified
		if mergeStrategy == "" && config.DefaultMergeStrategy != "" {
			mergeStrategy = config.DefaultMergeStrategy
		}
		if mergeStrategy == "" {
			mergeStrategy = "ff-only"
		}

		// Show what we're about to do
		fmt.Printf("Merging '%s' into '%s' using %s...\n", target.Branch, defaultBranch, mergeStrategy)

		if err := mergeAndDeleteWorktree(repoPath, target, defaultBranch, mergeStrategy); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		fmt.Printf("Successfully merged '%s' into '%s' and deleted worktree\n", target.Branch, defaultBranch)
		os.Exit(0)
	}

	// Handle worktree creation if name provided
	if worktreeName != "" {
		// Get repo path
		repoPath, err := getCurrentRepoPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		// Load config
		config, _ := loadConfig()
		if config == nil {
			config = &Config{}
		}

		// Create the worktree.
		var worktreePath string
		if sourceBranch != "" {
			fmt.Printf("Creating worktree '%s' from branch '%s'...\n", worktreeName, sourceBranch)
			worktreePath, err = createWorktreeFromBranch(repoPath, worktreeName, sourceBranch, config)
		} else {
			fmt.Printf("Creating worktree '%s'...\n", worktreeName)
			worktreePath, err = createWorktree(repoPath, worktreeName, config)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			os.Exit(1)
		}

		// Run post-create hook if configured
		if err := runPostCreateHook(worktreePath, config); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: post-create hook failed: %v\n", err)
		}

		// Switch to the new worktree
		if err := switchToWorktree(worktreePath, config, executeCmd); err != nil {
			fmt.Fprintf(os.Stderr, "Error switching to worktree: %v\n", err)
			os.Exit(1)
		}
		os.Exit(0)
	}

	// No arguments - run interactive mode
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
