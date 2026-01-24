package main

import (
	"encoding/json"
	"errors"
	"fmt"
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
)

const (
	version            = "0.4.0"
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
)

// Config holds user configuration loaded from ~/.config/gt/config.json.
type Config struct {
	WorktreeDir          string `json:"worktree_dir,omitempty"`
	Shell                string `json:"shell,omitempty"`
	PostCreate           string `json:"post_create,omitempty"`
	DefaultMergeStrategy string `json:"default_merge_strategy,omitempty"` // "squash" or "ff-only"
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
}

// CommitInfo holds metadata about a git commit.
type CommitInfo struct {
	Hash    string
	Message string
	Date    time.Time
	Author  string
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
	worktrees        []Worktree
	filtered         []Worktree
	searchTerm       string
	defaultBranch    string
	previousWorktree string
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
	ui       uiState
	wt       worktreeState
	acts     actionsState
	del      deleteState
	merge    mergeState
	repoPath string
	config   *Config
	err      error
	status   struct {
		message string
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

// getAheadBehind returns the number of commits the branch is ahead/behind the default branch.
// Returns (0, 0) if the branch is the default branch, or if any error occurs during git operations.
func getAheadBehind(worktreePath, branch, defaultBranch string) (ahead, behind int) {
	if branch == "" || branch == defaultBranch {
		return 0, 0
	}

	// Get count of commits ahead and behind
	cmd := exec.Command("git", "rev-list", "--left-right", "--count", fmt.Sprintf("%s...%s", defaultBranch, branch))
	cmd.Dir = worktreePath
	output, err := cmd.Output()
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

func getWorktrees(repoPath string) ([]Worktree, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	// Get default branch early for ahead/behind calculation
	defaultBranch := getDefaultBranch(repoPath)

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

	// Get additional info for each worktree
	for i := range worktrees {
		worktrees[i].IsCurrent = strings.HasPrefix(cwd, worktrees[i].Path)

		// Check if dirty
		cmd := exec.Command("git", "status", "--porcelain")
		cmd.Dir = worktrees[i].Path
		output, err := cmd.Output()
		if err == nil {
			worktrees[i].IsDirty = len(strings.TrimSpace(string(output))) > 0
		}

		// Get last commit info
		cmd = exec.Command("git", "log", "-1", "--pretty=format:%H|%s|%ai|%an")
		cmd.Dir = worktrees[i].Path
		output, err = cmd.Output()
		if err == nil && len(output) > 0 {
			parts := strings.Split(string(output), "|")
			if len(parts) >= 4 {
				hash := parts[0]
				if len(hash) > hashDisplayLength {
					hash = hash[:hashDisplayLength]
				}
				worktrees[i].LastCommit.Hash = hash
				worktrees[i].LastCommit.Message = parts[1]
				if t, err := time.Parse("2006-01-02 15:04:05 -0700", parts[2]); err == nil {
					worktrees[i].LastCommit.Date = t
				}
				worktrees[i].LastCommit.Author = parts[3]
			}
		}

		// Get ahead/behind count relative to default branch
		worktrees[i].Ahead, worktrees[i].Behind = getAheadBehind(worktrees[i].Path, worktrees[i].Branch, defaultBranch)
	}

	return worktrees, nil
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

	worktrees, err := getWorktrees(repoPath)
	if err != nil {
		return model{err: err}
	}

	// Get default branch for ^ shortcut
	defaultBranch := getDefaultBranch(repoPath)

	// Find current worktree path for tracking previous (session-only)
	var currentPath string
	for _, wt := range worktrees {
		if wt.IsCurrent {
			currentPath = wt.Path
			break
		}
	}

	return model{
		ui: uiState{},
		wt: worktreeState{
			worktrees:        worktrees,
			filtered:         worktrees,
			defaultBranch:    defaultBranch,
			previousWorktree: currentPath,
		},
		acts: actionsState{
			actions: getActions(),
		},
		repoPath: repoPath,
		config:   config,
	}
}

func (m model) Init() tea.Cmd {
	return nil
}

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

// setStatus sets a status message that will be displayed temporarily.
func (m *model) setStatus(msg string, timeout time.Duration) {
	m.status.message = msg
	m.status.timeout = time.Now().Add(timeout)
}

// refreshWorktrees reloads the worktree list from git.
func (m *model) refreshWorktrees() {
	worktrees, err := getWorktrees(m.repoPath)
	if err != nil {
		m.setStatus(fmt.Sprintf("Refresh failed: %v", err), statusMessageTimeout)
		return
	}
	m.wt.worktrees = worktrees
	m.wt.filtered = filterWorktrees(worktrees, m.wt.searchTerm)
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
		if m.del.target != nil {
			if err := deleteWorktree(m.repoPath, m.del.target.Path); err != nil {
				m.err = err
			} else {
				m.setStatus(fmt.Sprintf("Deleted worktree: %s", m.del.target.Branch), statusMessageTimeout)
				m.refreshWorktrees()
			}
		}
		m.del.confirming = false
		m.del.target = nil
		return m, tickCmd()
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
		if m.ui.inputValue != "" {
			if err := validateBranchName(m.ui.inputValue); err != nil {
				m.err = err
			} else if err := createWorktree(m.repoPath, m.ui.inputValue, m.config); err != nil {
				m.err = err
			} else {
				m.setStatus(fmt.Sprintf("Created worktree: %s", m.ui.inputValue), statusMessageTimeout)
				m.refreshWorktrees()
			}
		}
		m.ui.inputMode = modeNormal
		m.ui.inputValue = ""
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
		m.setStatus(fmt.Sprintf("Action failed: %v", err), statusMessageTimeout)
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
		if m.merge.target != nil {
			if err := mergeAndDeleteWorktree(m.repoPath, m.merge.target, m.wt.defaultBranch, m.merge.strategy); err != nil {
				m.err = err
			} else {
				m.setStatus(fmt.Sprintf("Merged '%s' into '%s' and deleted worktree", m.merge.target.Branch, m.wt.defaultBranch), statusMessageTimeout)
				m.refreshWorktrees()
			}
		}
		m.ui.inputMode = modeNormal
		m.merge.target = nil
		m.merge.strategy = ""
		return m, tickCmd()
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
				m.err = fmt.Errorf("cannot delete main worktree")
				return m, nil
			}
			m.del.confirming = true
			m.del.target = wt
		}
		return m, nil

	case "r":
		m.refreshWorktrees()
		m.setStatus("Refreshed", refreshTimeout)
		return m, tickCmd()

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
				m.err = fmt.Errorf("cannot merge main worktree")
				return m, nil
			}
			if wt.Branch == m.wt.defaultBranch {
				m.err = fmt.Errorf("cannot merge default branch into itself")
				return m, nil
			}
			if wt.IsCurrent {
				m.err = fmt.Errorf("cannot merge currently checked out worktree")
				return m, nil
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
				m.err = err
				return m, nil
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

	case tickMsg:
		if !m.status.timeout.IsZero() && time.Now().After(m.status.timeout) {
			m.status.message = ""
			m.status.timeout = time.Time{}
		}
		return m, tickCmd()

	case tea.KeyMsg:
		// Clear any previous operational error on keypress
		m.err = nil

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

func createWorktreeFromBranch(repoPath, worktreeName, sourceBranch string, config *Config) error {
	// Determine worktree directory
	worktreeDir := defaultWorktreeDir
	if config != nil && config.WorktreeDir != "" {
		worktreeDir = config.WorktreeDir
	}

	// Handle absolute vs relative paths
	if !filepath.IsAbs(worktreeDir) {
		worktreeDir = filepath.Join(repoPath, worktreeDir)
	}

	// Create worktree directory if it doesn't exist
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		return err
	}

	// Add worktree directory to git excludes if it's within the repo
	if strings.HasPrefix(worktreeDir, repoPath) {
		relPath, _ := filepath.Rel(repoPath, worktreeDir)
		if relPath != "" && !strings.HasPrefix(relPath, "..") {
			if err := ensureNoTrackedFiles(repoPath, relPath); err != nil {
				return err
			}
			if err := ensureGitExcludeEntry(repoPath, relPath); err != nil {
				// Don't fail the worktree creation if we can't update git excludes
				// Just continue with a warning
				fmt.Fprintf(os.Stderr, "Warning: Could not update git excludes: %v\n", err)
			}
		}
	}

	// Generate worktree path
	worktreePath := filepath.Join(worktreeDir, strings.ReplaceAll(worktreeName, "/", "-"))

	// Create worktree with new branch from source branch
	cmd := exec.Command("git", "worktree", "add", "-b", worktreeName, worktreePath, sourceBranch)
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		// If branch already exists, try without -b flag
		cmd = exec.Command("git", "worktree", "add", worktreePath, worktreeName)
		cmd.Dir = repoPath
		output, err = cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("failed to create worktree: %s", string(output))
		}
	}

	return nil
}

func createWorktree(repoPath, branch string, config *Config) error {
	// Determine worktree directory
	worktreeDir := defaultWorktreeDir
	if config != nil && config.WorktreeDir != "" {
		worktreeDir = config.WorktreeDir
	}

	// Handle absolute vs relative paths
	if !filepath.IsAbs(worktreeDir) {
		worktreeDir = filepath.Join(repoPath, worktreeDir)
	}

	// Create worktree directory if it doesn't exist
	if err := os.MkdirAll(worktreeDir, 0755); err != nil {
		return err
	}

	// Add worktree directory to git excludes if it's within the repo
	if strings.HasPrefix(worktreeDir, repoPath) {
		relPath, _ := filepath.Rel(repoPath, worktreeDir)
		if relPath != "" && !strings.HasPrefix(relPath, "..") {
			if err := ensureNoTrackedFiles(repoPath, relPath); err != nil {
				return err
			}
			if err := ensureGitExcludeEntry(repoPath, relPath); err != nil {
				// Don't fail the worktree creation if we can't update git excludes
				// Just continue with a warning
				fmt.Fprintf(os.Stderr, "Warning: Could not update git excludes: %v\n", err)
			}
		}
	}

	// Generate worktree path
	worktreePath := filepath.Join(worktreeDir, strings.ReplaceAll(branch, "/", "-"))

	// Check if branch exists locally or remotely
	cmd := exec.Command("git", "rev-parse", "--verify", branch)
	cmd.Dir = repoPath
	if err := cmd.Run(); err != nil {
		// Try as remote branch
		cmd = exec.Command("git", "ls-remote", "--heads", "origin", branch)
		cmd.Dir = repoPath
		output, err := cmd.Output()
		if err != nil || len(output) == 0 {
			// Create new branch
			cmd = exec.Command("git", "worktree", "add", "-b", branch, worktreePath)
		} else {
			// Checkout existing remote branch
			cmd = exec.Command("git", "worktree", "add", worktreePath, branch)
		}
	} else {
		// Checkout existing local branch
		cmd = exec.Command("git", "worktree", "add", worktreePath, branch)
	}

	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("failed to create worktree: %s", string(output))
	}

	return nil
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

			// Branch name
			branch := wt.Branch
			if branch == "" {
				branch = "(detached)"
			}
			if wt.IsCurrent {
				branch = currentStyle.Render("● " + branch)
			} else {
				branch = branchStyle.Render(branch)
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

			// Commit info
			commitMsg := truncateString(wt.LastCommit.Message, maxCommitMsgLength)
			relTime := formatRelativeTime(wt.LastCommit.Date)

			// Format line
			line := fmt.Sprintf("%s%-*s %s%s  %s",
				cursor,
				branchDisplayWidth,
				branch,
				status,
				aheadBehind,
				dimStyle.Render(fmt.Sprintf("%s (%s)", commitMsg, relTime)),
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
		s.WriteString("\n" + successStyle.Render(m.status.message) + "\n")
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
    gt <name>                    Create worktree from current branch and switch to it
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
    gt feature-xyz               # Create worktree 'feature-xyz' from current branch
    gt fix-bug main              # Create worktree 'fix-bug' from 'main' branch
    gt feature-xyz -x "code ."   # Create worktree and open in VS Code
    gt feature-xyz -x claude     # Create worktree and start Claude Code
    gt --merge feature-xyz       # Merge 'feature-xyz' into main (ff-only)
    gt --merge feature-xyz --squash  # Squash merge into main

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

func getCurrentBranch(repoPath string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func getDefaultBranch(repoPath string) string {
	// Try to get the default branch from remote
	cmd := exec.Command("git", "symbolic-ref", "refs/remotes/origin/HEAD")
	cmd.Dir = repoPath
	output, err := cmd.Output()
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
		cmd = exec.Command("git", "rev-parse", "--verify", branch)
		cmd.Dir = repoPath
		if err := cmd.Run(); err == nil {
			return branch
		}
	}

	return "main"
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

		// Determine source branch
		if sourceBranch == "" {
			// Use current branch
			sourceBranch, err = getCurrentBranch(repoPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error getting current branch: %v\n", err)
				os.Exit(1)
			}
		}

		// Create the worktree
		fmt.Printf("Creating worktree '%s' from branch '%s'...\n", worktreeName, sourceBranch)

		// First check if we need to create from an existing branch or create new
		if sourceBranch != worktreeName {
			// Create worktree from existing branch
			err = createWorktreeFromBranch(repoPath, worktreeName, sourceBranch, config)
		} else {
			// Create new branch with worktree
			err = createWorktree(repoPath, worktreeName, config)
		}

		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating worktree: %v\n", err)
			os.Exit(1)
		}

		// Determine worktree path
		worktreeDir := defaultWorktreeDir
		if config != nil && config.WorktreeDir != "" {
			worktreeDir = config.WorktreeDir
		}
		if !filepath.IsAbs(worktreeDir) {
			worktreeDir = filepath.Join(repoPath, worktreeDir)
		}
		worktreePath := filepath.Join(worktreeDir, strings.ReplaceAll(worktreeName, "/", "-"))

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
