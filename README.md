# gt - Git Worktree Manager

> A blazing fast TUI for managing Git worktrees with zero friction

Ever find yourself juggling multiple feature branches, hotfixes, and experiments? Constantly stashing, switching branches, and losing context?

**gt** makes Git worktrees as easy as breathing.

## What it does

Instantly manage all your Git worktrees with:
- **Interactive TUI** - See all worktrees at a glance
- **Instant creation** - `gt feature-x` creates and switches in one command
- **Smart organization** - Keeps worktrees in `.worktrees/` (auto-added to `.gitignore`)
- **Zero config** - Single binary, works out of the box

## Installation

### Homebrew (macOS)

```bash
brew tap melonamin/formulae
brew install gt
```

### Download Pre-built Binaries

Download the latest release from [GitHub Releases](https://github.com/melonamin/gt/releases):

#### macOS (Universal)
```bash
curl -L https://github.com/melonamin/gt/releases/latest/download/gt-macos-universal.zip -o gt.zip
unzip gt.zip
chmod +x gt-macos-universal
sudo mv gt-macos-universal /usr/local/bin/gt
```

#### Linux (amd64)
```bash
curl -L https://github.com/melonamin/gt/releases/latest/download/gt-linux-amd64.tar.gz | tar xz
chmod +x gt-linux-amd64
sudo mv gt-linux-amd64 /usr/local/bin/gt
```

#### Linux (arm64)
```bash
curl -L https://github.com/melonamin/gt/releases/latest/download/gt-linux-arm64.tar.gz | tar xz
chmod +x gt-linux-arm64
sudo mv gt-linux-arm64 /usr/local/bin/gt
```

### Using Go
```bash
go install github.com/melonamin/gt@latest
```

### Build from Source
```bash
git clone https://github.com/melonamin/gt.git
cd gt
go build -o ~/.local/bin/gt
```

## The Problem

You're working on a feature. Urgent bug comes in. You stash everything, switch branches, fix the bug, switch back, pop stash, resolve conflicts... 😤

Or worse: you have `repo`, `repo-backup`, `repo-new`, `repo-actually-working` scattered across your filesystem.

## The Solution

Each branch gets its own working directory. Switch instantly, no stashing:

```bash
$ gt
→ main               ✓  Initial commit (2 hours ago)
  feature-auth       ●  Add OAuth implementation (3 days ago)
  fix-memory-leak    ✓  Fix connection pooling (1 week ago)

[n]ew  [d]elete  [enter] switch  [/] search  [q]uit
```

## Quick Start

```bash
# In any git repository
gt                    # Open interactive worktree manager
gt feature-xyz        # Create worktree 'feature-xyz' from current branch and switch
gt fix-bug main       # Create worktree 'fix-bug' from 'main' branch and switch
```

That's it. No configuration needed.

## Features

### ⚡ Instant Worktree Creation
```bash
gt my-feature
```
Creates a new worktree from your current branch and switches to it immediately. Your original working directory stays untouched.

### 🎯 Interactive Management
```bash
gt
```
Opens a beautiful TUI showing all your worktrees with:
- Branch names and current status
- Last commit message and time
- Dirty state indicators (●)
- Current worktree highlighting

### 🔍 Smart Search
Press `/` in the TUI to fuzzy search through:
- Branch names
- Commit messages
- Worktree paths

### 🗂️ Automatic Organization
Worktrees are stored in `.worktrees/` within your repo:
```
my-project/
├── .git/
├── .worktrees/       # All worktrees here (auto-added to .gitignore)
│   ├── feature-auth/
│   ├── fix-bug/
│   └── experiment/
└── ... your files
```

### 🎨 Visual Status
- `●` Current worktree (where you are now)
- `●` Dirty (uncommitted changes)
- `✓` Clean
- Time-aware display (2 hours ago, 3 days ago, etc.)

## Usage

### Command Line
```bash
gt                           # Open interactive worktree manager
gt <name>                    # Create worktree from current branch and switch
gt <name> <branch>           # Create worktree from specified branch and switch
gt -h, --help                # Show help
```

### Interactive Mode (TUI)

| Key | Action |
|-----|--------|
| `↑/↓` or `j/k` | Navigate list |
| `Enter` | Switch to selected worktree |
| `n` | Create new worktree |
| `d` | Delete selected worktree |
| `/` | Search/filter worktrees |
| `r` | Refresh list |
| `q` or `Ctrl+C` | Quit |

### Examples

```bash
# Working on a feature, need to fix a bug
gt hotfix-memory-leak main    # Creates worktree from main, switches there

# After fixing, go back
gt                            # Open TUI, select your feature, press Enter

# Start a new feature
gt new-oauth-flow             # Creates from current branch

# Experiment without affecting anything
gt experiment-redis-cache develop
```

## Configuration

Configuration file: `~/.config/gt/config.json`

```json
{
  "worktree_dir": ".worktrees",  // Where to store worktrees (relative or absolute)
  "shell": "fish"                 // Override shell (default: $SHELL)
}
```

### Environment Variables

The shell used when switching worktrees:
1. Config file `shell` setting (if set)
2. `$SHELL` environment variable
3. Fallback to `/bin/bash`

## How It Works

Git worktrees let you have multiple working directories for the same repository. Instead of stashing/switching branches, you just switch directories. Each worktree has:
- Its own working directory
- Its own index (staging area)
- Its own HEAD (current branch)
- Shared repository data (.git)

`gt` makes worktrees frictionless by:
1. Organizing them in one place
2. Providing instant creation/switching
3. Showing status at a glance
4. Auto-managing `.gitignore`

## Comparison with Alternatives

| Feature | `git worktree` | `git stash` | **gt** |
|---------|---------------|-------------|--------|
| **Switch Speed** | Manual `cd` | Slow (stash/pop) | Instant |
| **Context Preservation** | ✅ Full | ❌ Lost | ✅ Full |
| **Discoverability** | `git worktree list` | `git stash list` | Visual TUI |
| **Creation** | Multi-step | N/A | One command |
| **Organization** | DIY | N/A | Automatic |
| **Learning Curve** | Medium | Low | Zero |

## FAQ

**Q: How is this different from just using `git worktree`?**
A: `gt` is to `git worktree` what `tig` is to `git log`. It makes the powerful feature actually pleasant to use.

**Q: Where are my worktrees stored?**
A: By default in `.worktrees/` within your repo. This is configurable and automatically added to `.gitignore`.

**Q: Can I use my existing worktrees?**
A: Yes! `gt` shows all worktrees, regardless of where they were created.

**Q: What happens to the worktree when I delete a branch?**
A: The worktree remains but becomes detached. You can delete it with `gt` (press `d` in the TUI).

**Q: Does this work with bare repositories?**
A: Yes, it works with any Git repository structure.

## Philosophy

Worktrees are Git's best-kept secret. They solve the context-switching problem elegantly, but the CLI is clunky. `gt` makes them feel native.

No more:
- Lost stashes
- Conflicted merges after stash pop
- Uncommitted changes blocking branch switches
- Mental overhead of context switching

Just instant, clean switches between parallel workstreams.

## Contributing

The entire tool is a single Go file using the excellent [Bubble Tea](https://github.com/charmbracelet/bubbletea) TUI framework. PRs welcome!

## License

MIT - Do whatever you want with it.
