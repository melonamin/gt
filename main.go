package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	defaultWorktreeDir = ".worktrees"
	configFileName     = "config.json"
	configDirName      = "worktree-manager"
)

type Config struct {
	WorktreeDir string `json:"worktree_dir,omitempty"`
	Shell       string `json:"shell,omitempty"`
}

type Worktree struct {
	Path       string
	Branch     string
	Head       string
	IsDirty    bool
	LastCommit CommitInfo
	IsCurrent  bool
}

type CommitInfo struct {
	Hash    string
	Message string
	Date    time.Time
	Author  string
}

type model struct {
	worktrees      []Worktree
	filtered       []Worktree
	cursor         int
	scrollOffset   int
	searchTerm     string
	width          int
	height         int
	quitting       bool
	inputMode      inputMode
	inputValue     string
	confirmDelete  bool
	deleteTarget   *Worktree
	repoPath       string
	config         *Config
	err            error
	statusMessage  string
	statusTimeout  time.Time
}

type inputMode int

const (
	modeNormal inputMode = iota
	modeSearch
	modeNewBranch
	modeNewPath
)

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
)

func getConfigPath() string {
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

func getWorktrees(repoPath string) ([]Worktree, error) {
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repoPath
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

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
				worktrees[i].LastCommit.Hash = parts[0][:7]
				worktrees[i].LastCommit.Message = parts[1]
				if t, err := time.Parse("2006-01-02 15:04:05 -0700", parts[2]); err == nil {
					worktrees[i].LastCommit.Date = t
				}
				worktrees[i].LastCommit.Author = parts[3]
			}
		}
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

	m := model{
		worktrees: worktrees,
		filtered:  worktrees,
		repoPath:  repoPath,
		config:    config,
	}

	return m
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

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		if !m.statusTimeout.IsZero() && time.Now().After(m.statusTimeout) {
			m.statusMessage = ""
			m.statusTimeout = time.Time{}
		}
		return m, tickCmd()

	case tea.KeyMsg:
		if m.confirmDelete {
			switch msg.String() {
			case "y", "Y":
				if m.deleteTarget != nil {
					if err := deleteWorktree(m.repoPath, m.deleteTarget.Path); err != nil {
						m.err = err
					} else {
						m.statusMessage = fmt.Sprintf("Deleted worktree: %s", m.deleteTarget.Branch)
						m.statusTimeout = time.Now().Add(3 * time.Second)
						// Refresh worktrees
						if worktrees, err := getWorktrees(m.repoPath); err == nil {
							m.worktrees = worktrees
							m.filtered = filterWorktrees(worktrees, m.searchTerm)
						}
					}
				}
				m.confirmDelete = false
				m.deleteTarget = nil
				return m, tickCmd()
			case "n", "N", "esc", "ctrl+c":
				m.confirmDelete = false
				m.deleteTarget = nil
				return m, nil
			}
			return m, nil
		}

		switch m.inputMode {
		case modeSearch:
			switch msg.String() {
			case "esc", "ctrl+c":
				m.inputMode = modeNormal
				m.searchTerm = ""
				m.filtered = m.worktrees
				return m, nil
			case "enter":
				m.inputMode = modeNormal
				return m, nil
			case "backspace":
				if len(m.searchTerm) > 0 {
					m.searchTerm = m.searchTerm[:len(m.searchTerm)-1]
					m.filtered = filterWorktrees(m.worktrees, m.searchTerm)
				}
				return m, nil
			default:
				if len(msg.String()) == 1 {
					m.searchTerm += msg.String()
					m.filtered = filterWorktrees(m.worktrees, m.searchTerm)
				}
				return m, nil
			}

		case modeNewBranch:
			switch msg.String() {
			case "esc", "ctrl+c":
				m.inputMode = modeNormal
				m.inputValue = ""
				return m, nil
			case "enter":
				if m.inputValue != "" {
					if err := createWorktree(m.repoPath, m.inputValue, m.config); err != nil {
						m.err = err
					} else {
						m.statusMessage = fmt.Sprintf("Created worktree: %s", m.inputValue)
						m.statusTimeout = time.Now().Add(3 * time.Second)
						// Refresh worktrees
						if worktrees, err := getWorktrees(m.repoPath); err == nil {
							m.worktrees = worktrees
							m.filtered = filterWorktrees(worktrees, m.searchTerm)
						}
					}
				}
				m.inputMode = modeNormal
				m.inputValue = ""
				return m, tickCmd()
			case "backspace":
				if len(m.inputValue) > 0 {
					m.inputValue = m.inputValue[:len(m.inputValue)-1]
				}
				return m, nil
			default:
				if len(msg.String()) == 1 || msg.String() == "/" || msg.String() == "-" {
					m.inputValue += msg.String()
				}
				return m, nil
			}

		default: // modeNormal
			switch msg.String() {
			case "q", "ctrl+c":
				m.quitting = true
				return m, tea.Quit

			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
				return m, nil

			case "down", "j":
				if m.cursor < len(m.filtered)-1 {
					m.cursor++
				}
				return m, nil

			case "/":
				m.inputMode = modeSearch
				m.searchTerm = ""
				return m, nil

			case "n":
				m.inputMode = modeNewBranch
				m.inputValue = ""
				return m, nil

			case "d":
				if m.cursor < len(m.filtered) {
					wt := &m.filtered[m.cursor]
					if strings.HasSuffix(m.repoPath, wt.Path) {
						m.err = fmt.Errorf("cannot delete main worktree")
						return m, nil
					}
					m.confirmDelete = true
					m.deleteTarget = wt
				}
				return m, nil

			case "r":
				// Refresh worktrees
				if worktrees, err := getWorktrees(m.repoPath); err == nil {
					m.worktrees = worktrees
					m.filtered = filterWorktrees(worktrees, m.searchTerm)
					m.statusMessage = "Refreshed"
					m.statusTimeout = time.Now().Add(2 * time.Second)
				}
				return m, tickCmd()

			case "enter":
				if m.cursor < len(m.filtered) {
					wt := m.filtered[m.cursor]
					// Exit the TUI and switch to the worktree
					m.quitting = true
					fmt.Printf("\n\033[2mSwitching to %s...\033[0m\n", wt.Path)
					
					// Change to the worktree directory
					if err := os.Chdir(wt.Path); err != nil {
						m.err = err
						return m, nil
					}
					
					// Start a new shell in the worktree directory
					shell := getShell(m.config)
					cmd := exec.Command(shell)
					cmd.Stdin = os.Stdin
					cmd.Stdout = os.Stdout
					cmd.Stderr = os.Stderr
					cmd.Dir = wt.Path
					
					// Execute the shell after quitting the TUI
					return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
						if err != nil {
							return err
						}
						return tea.Quit()
					})
				}
				return m, nil
			}
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

func ensureGitignoreEntry(repoPath, entry string) error {
	gitignorePath := filepath.Join(repoPath, ".gitignore")
	
	// Ensure entry ends with / if it's a directory
	if !strings.HasSuffix(entry, "/") {
		entry = entry + "/"
	}
	
	// Read existing .gitignore content
	content, err := os.ReadFile(gitignorePath)
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
	
	// Add entry to .gitignore
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
	
	return os.WriteFile(gitignorePath, []byte(newContent), 0644)
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

	// Add worktree directory to .gitignore if it's within the repo
	if strings.HasPrefix(worktreeDir, repoPath) {
		relPath, _ := filepath.Rel(repoPath, worktreeDir)
		if relPath != "" && !strings.HasPrefix(relPath, "..") {
			if err := ensureGitignoreEntry(repoPath, relPath); err != nil {
				// Don't fail the worktree creation if we can't update .gitignore
				// Just continue with a warning
				fmt.Fprintf(os.Stderr, "Warning: Could not update .gitignore: %v\n", err)
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

func (m model) View() string {
	if m.err != nil {
		return errorStyle.Render(fmt.Sprintf("Error: %v\n\nPress q to quit.", m.err))
	}

	var s strings.Builder

	// Title
	title := fmt.Sprintf("Git Worktrees - %s", m.repoPath)
	s.WriteString(titleStyle.Render(title) + "\n")

	// Search or input
	switch m.inputMode {
	case modeSearch:
		s.WriteString(searchStyle.Render("Search: ") + m.searchTerm + "█\n\n")
	case modeNewBranch:
		s.WriteString(searchStyle.Render("New branch name: ") + m.inputValue + "█\n\n")
	default:
		if m.searchTerm != "" {
			s.WriteString(searchStyle.Render("Search: ") + dimStyle.Render(m.searchTerm) + "\n\n")
		} else {
			s.WriteString("\n")
		}
	}

	// Confirmation dialog
	if m.confirmDelete && m.deleteTarget != nil {
		s.WriteString(errorStyle.Render(fmt.Sprintf("\nDelete worktree '%s'? [y/N] ", m.deleteTarget.Branch)))
		return s.String()
	}

	// Worktree list
	if len(m.filtered) == 0 {
		s.WriteString(dimStyle.Render("  No worktrees found\n"))
	} else {
		viewportHeight := m.height - 8 // Account for header and footer
		if viewportHeight < 5 {
			viewportHeight = 5
		}

		// Adjust scroll offset
		if m.cursor >= m.scrollOffset+viewportHeight {
			m.scrollOffset = m.cursor - viewportHeight + 1
		} else if m.cursor < m.scrollOffset {
			m.scrollOffset = m.cursor
		}

		for i := m.scrollOffset; i < len(m.filtered) && i < m.scrollOffset+viewportHeight; i++ {
			wt := m.filtered[i]
			
			// Cursor indicator
			cursor := "  "
			if i == m.cursor {
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

			// Commit info
			commitMsg := wt.LastCommit.Message
			if len(commitMsg) > 40 {
				commitMsg = commitMsg[:40] + "..."
			}
			
			relTime := formatRelativeTime(wt.LastCommit.Date)

			// Format line
			line := fmt.Sprintf("%s%-20s %s  %s",
				cursor,
				branch,
				status,
				dimStyle.Render(fmt.Sprintf("%s (%s)", commitMsg, relTime)),
			)

			if i == m.cursor {
				s.WriteString(selectedStyle.Render(line) + "\n")
			} else {
				s.WriteString(line + "\n")
			}
		}
	}

	// Status message
	if m.statusMessage != "" {
		s.WriteString("\n" + successStyle.Render(m.statusMessage) + "\n")
	}

	// Help
	s.WriteString("\n")
	if m.inputMode == modeNormal {
		help := "[n]ew  [d]elete  [enter] switch  [/] search  [r]efresh  [q]uit"
		s.WriteString(helpStyle.Render(help))
	} else {
		help := "[enter] confirm  [esc] cancel"
		s.WriteString(helpStyle.Render(help))
	}

	return s.String()
}

func main() {
	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}