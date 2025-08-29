# gt - Git Worktree Manager

A simple TUI for managing git worktrees using Bubble Tea.

## Features

- List all worktrees with branch names and status
- Create new worktrees from branches
- Delete worktrees with confirmation
- Switch to worktree directory
- Search/filter worktrees
- Show commit info and dirty status
- Configurable worktree storage location

## Installation

```bash
go install github.com/alex/gt@latest
```

Or build from source:

```bash
just build
# or
go build -o gt main.go
```

## Usage

Run in any git repository:

```bash
gt
```

### Keyboard shortcuts

- `↑/↓` or `j/k` - Navigate list
- `Enter` - Switch to selected worktree
- `n` - Create new worktree
- `d` - Delete worktree
- `/` - Search/filter
- `r` - Refresh list
- `q` - Quit

## Configuration

Configuration is stored in `~/.config/worktree-manager/config.json`

```json
{
  "worktree_dir": ".worktrees",  // Where to store worktrees (relative or absolute)
  "shell": "/bin/zsh"             // Shell to use when switching
}
```

By default, worktrees are stored in `.worktrees/` directory in the repository root.

## How it works

- Uses `git worktree` commands under the hood
- Stores worktrees in a configurable directory (default: `.worktrees/`)
- Shows real-time status (clean/dirty) for each worktree
- Displays last commit message and relative time