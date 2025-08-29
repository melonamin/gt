# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.0] - 2025-01-29

### 🎉 Initial Release

The first public release of `gt` - a blazing fast TUI for managing Git worktrees with zero friction.

### Added

#### Core Features
- **Interactive TUI** for managing Git worktrees with Bubble Tea framework
- **Instant worktree creation** with `gt <name>` command for immediate creation and switching
- **Branch-based creation** with `gt <name> <branch>` to create from specific branches
- **Visual worktree management** showing all worktrees with status indicators
- **Real-time status display** including:
  - Current worktree highlighting (●)
  - Dirty state indicators for uncommitted changes
  - Clean state indicators (✓)
  - Last commit messages and relative timestamps
- **Smart search/filtering** with `/` key for fuzzy finding across:
  - Branch names
  - Commit messages
  - Worktree paths

#### Organization & Automation
- **Automatic `.gitignore` management** - adds `.worktrees/` on first use
- **Configurable worktree directory** (default: `.worktrees/`)
- **Smart path handling** for both relative and absolute paths
- **Automatic directory creation** with proper permissions

#### User Experience
- **Zero configuration** - works out of the box
- **Keyboard navigation** with intuitive shortcuts:
  - `j/k` or arrow keys for navigation
  - `n` for new worktree
  - `d` for delete with confirmation
  - `r` for refresh
  - `Enter` to switch
- **Time-aware display** showing human-readable timestamps ("2 hours ago", "3 days ago")
- **Shell integration** that respects `$SHELL` environment variable
- **Comprehensive help** with `gt --help`

#### Configuration
- **Config file support** at `~/.config/gt/config.json`
- **Customizable shell** override option
- **Configurable worktree storage location**

#### Build & Distribution
- **Single binary distribution** with no runtime dependencies
- **Cross-platform support** for macOS (universal) and Linux (amd64/arm64)
- **Signed and notarized macOS builds** for security
- **Automated release pipeline** with checksums

### Technical Details
- Written in Go for performance and portability
- Uses Bubble Tea for elegant TUI
- Leverages native `git worktree` commands
- Minimal resource footprint
- Fast startup and response times

### Platform Support
- macOS (Intel and Apple Silicon)
- Linux (amd64 and arm64)
- Any platform that supports Go compilation

---
