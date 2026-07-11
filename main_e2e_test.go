package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func buildBinary(t *testing.T) string {
	t.Helper()

	projectRoot, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working dir: %v", err)
	}

	binDir := t.TempDir()
	binaryPath := filepath.Join(binDir, "gt")

	cmd := exec.Command("go", "build", "-o", binaryPath, ".")
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build failed: %v\n%s", err, output)
	}

	return binaryPath
}

func TestGTRunCreatesWorktreeAndUpdatesExcludes(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "-x", "true", "feature-branch")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-branch")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree to exist: %v", err)
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
}

func TestGTRunFromLinkedWorktreeUsesMainRelativeWorktreeDir(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)
	configHome := t.TempDir()

	runGT := func(dir string, args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binaryPath, args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("gt %v failed from %s: %v\n%s", args, dir, err, output)
		}
		return output
	}

	runGT(repoPath, "-x", "true", "feature-one")

	firstWorktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-one")
	featureFile := filepath.Join(firstWorktreePath, "from-feature-one.txt")
	if err := os.WriteFile(featureFile, []byte("created on feature one"), 0644); err != nil {
		t.Fatalf("write feature file: %v", err)
	}
	runGit(t, firstWorktreePath, "add", "from-feature-one.txt")
	runGit(t, firstWorktreePath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "feature one commit")

	runGT(firstWorktreePath, "-x", "true", "feature-two")

	flatWorktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-two")
	if _, err := os.Stat(flatWorktreePath); err != nil {
		t.Fatalf("expected worktree at main worktree-relative path: %v", err)
	}

	nestedWorktreePath := filepath.Join(firstWorktreePath, defaultWorktreeDir, "feature-two")
	if _, err := os.Stat(nestedWorktreePath); err == nil {
		t.Fatalf("did not expect nested worktree at %s", nestedWorktreePath)
	}

	if _, err := os.Stat(filepath.Join(flatWorktreePath, "from-feature-one.txt")); err != nil {
		t.Fatalf("expected new worktree to branch from linked worktree HEAD: %v", err)
	}
}

func TestGTRunPostCreateHookUsesCreatedWorktreePath(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)
	configHome := t.TempDir()
	hookPathFile := filepath.Join(t.TempDir(), "hook-path")

	configDir := filepath.Join(configHome, "gt")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config directory: %v", err)
	}
	configPath := filepath.Join(configDir, "config.json")
	configData := `{"shell":"/bin/sh","post_create":"pwd > \"$GT_HOOK_CWD\""}`
	if err := os.WriteFile(configPath, []byte(configData), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	linkedWorktreePath := filepath.Join(repoPath, defaultWorktreeDir, "source")
	runGit(t, repoPath, "worktree", "add", "-b", "source", linkedWorktreePath)

	cmd := exec.Command(binaryPath, "-x", "true", "feature-branch")
	cmd.Dir = linkedWorktreePath
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+configHome, "GT_HOOK_CWD="+hookPathFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	hookPath, err := os.ReadFile(hookPathFile)
	if err != nil {
		t.Fatalf("read hook path: %v", err)
	}
	gotPath, err := filepath.EvalSymlinks(strings.TrimSpace(string(hookPath)))
	if err != nil {
		t.Fatalf("resolve hook path: %v", err)
	}
	wantPath, err := filepath.EvalSymlinks(filepath.Join(repoPath, defaultWorktreeDir, "feature-branch"))
	if err != nil {
		t.Fatalf("resolve expected worktree path: %v", err)
	}
	if string(gotPath) != wantPath {
		t.Errorf("post-create hook ran in %q, want %q", gotPath, wantPath)
	}
}

func TestGTRunFromSeparateGitDirUsesCheckoutRelativeWorktreeDir(t *testing.T) {
	parentDir := t.TempDir()
	repoPath := filepath.Join(parentDir, "checkout")
	gitDir := filepath.Join(parentDir, "metadata", ".git")
	if err := os.MkdirAll(filepath.Dir(gitDir), 0755); err != nil {
		t.Fatalf("create metadata directory: %v", err)
	}

	runGit(t, parentDir, "init", "--separate-git-dir="+gitDir, "--initial-branch=master", repoPath)
	readmePath := filepath.Join(repoPath, "README.md")
	if err := os.WriteFile(readmePath, []byte("test"), 0644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	runGit(t, repoPath, "add", "README.md")
	runGit(t, repoPath, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-m", "init")

	binaryPath := buildBinary(t)
	cmd := exec.Command(binaryPath, "-x", "true", "feature-branch")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-branch")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree to exist in checkout-relative path: %v", err)
	}
}

func TestGTRunRejectsRepositoryRootWorktreeDir(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)
	configDir := filepath.Join(t.TempDir(), "gt")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"worktree_dir":"."}`), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binaryPath, "-x", "true", "feature-branch")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Dir(configDir))
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected gt to reject repository-root worktree directory, got success:\n%s", output)
	}
	if !strings.Contains(string(output), "worktree directory must not be the repository root") {
		t.Fatalf("expected repository-root worktree directory error, got:\n%s", output)
	}

	branchOutput := runGit(t, repoPath, "branch", "--list", "feature-branch")
	if strings.TrimSpace(string(branchOutput)) != "" {
		t.Fatalf("expected feature branch not to be created, got %q", branchOutput)
	}
}

func TestGTRunCreatesWorktreeFromRemoteBranch(t *testing.T) {
	seedPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Seed a bare origin with only the default branch.
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	defaultBranch := strings.TrimSpace(string(runGit(t, seedPath, "rev-parse", "--abbrev-ref", "HEAD")))
	runGit(t, seedPath, "init", "--bare", remotePath)
	runGit(t, seedPath, "remote", "add", "origin", remotePath)
	runGit(t, seedPath, "push", "-u", "origin", defaultBranch)
	runGit(t, remotePath, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)

	// This is the repo where the user will run gt. It is cloned before the
	// remote-only branch exists, so it cannot already know about that branch.
	targetParent := t.TempDir()
	targetPath := filepath.Join(targetParent, "target")
	runGit(t, targetParent, "clone", remotePath, targetPath)

	// Simulate someone else creating and pushing the branch on the remote.
	publisherParent := t.TempDir()
	publisherPath := filepath.Join(publisherParent, "publisher")
	runGit(t, publisherParent, "clone", remotePath, publisherPath)
	runGit(t, publisherPath, "checkout", "-b", "remote-feature")
	remoteFile := filepath.Join(publisherPath, "remote.txt")
	if err := os.WriteFile(remoteFile, []byte("from remote"), 0644); err != nil {
		t.Fatalf("write remote file: %v", err)
	}
	runGit(t, publisherPath, "add", "remote.txt")
	runGit(t, publisherPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "remote feature")
	runGit(t, publisherPath, "push", "origin", "remote-feature")

	// Prove the target clone has neither a local branch nor a remote-tracking
	// branch before gt runs. gt must discover and fetch the remote branch.
	localBranch := strings.TrimSpace(string(runGit(t, targetPath, "branch", "--list", "remote-feature")))
	if localBranch != "" {
		t.Fatalf("target clone unexpectedly has local branch: %q", localBranch)
	}
	remoteTrackingBranch := strings.TrimSpace(string(runGit(t, targetPath, "branch", "-r", "--list", "origin/remote-feature")))
	if remoteTrackingBranch != "" {
		t.Fatalf("target clone unexpectedly has remote-tracking branch: %q", remoteTrackingBranch)
	}

	// Running gt with just the branch name should create a worktree from the
	// remote branch, not create a new same-named branch from the current HEAD.
	cmd := exec.Command(binaryPath, "-x", "true", "remote-feature")
	cmd.Dir = targetPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(targetPath, defaultWorktreeDir, "remote-feature")
	if _, err := os.Stat(filepath.Join(worktreePath, "remote.txt")); err != nil {
		t.Fatalf("expected worktree to contain remote branch file: %v", err)
	}

	// The local branch should track the remote branch, matching git switch's
	// remote-guess behavior.
	upstream := strings.TrimSpace(string(runGit(t, targetPath,
		"rev-parse", "--abbrev-ref", "--symbolic-full-name", "remote-feature@{upstream}")))
	if upstream != "origin/remote-feature" {
		t.Fatalf("upstream = %q, want origin/remote-feature", upstream)
	}
}

// publishBranch creates `branchName` in publisher, commits a file, and pushes
// it to every remote configured in publisher.
func publishBranch(t *testing.T, publisherPath, branchName, fileName, fileContents string, remotes ...string) {
	t.Helper()
	runGit(t, publisherPath, "checkout", "-b", branchName)
	if err := os.WriteFile(filepath.Join(publisherPath, fileName), []byte(fileContents), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	runGit(t, publisherPath, "add", fileName)
	runGit(t, publisherPath, "-c", "user.name=Test", "-c", "user.email=test@example.com",
		"commit", "-m", "publish "+branchName)
	for _, remote := range remotes {
		runGit(t, publisherPath, "push", remote, branchName)
	}
	runGit(t, publisherPath, "checkout", "master")
}

// initBareWithSeed sets up a bare origin populated by `seedPath` and returns the
// bare repo path with HEAD pointing at the default branch.
func initBareWithSeed(t *testing.T, seedPath string) string {
	t.Helper()
	remotePath := filepath.Join(t.TempDir(), "origin.git")
	defaultBranch := strings.TrimSpace(string(runGit(t, seedPath, "rev-parse", "--abbrev-ref", "HEAD")))
	runGit(t, seedPath, "init", "--bare", remotePath)
	runGit(t, seedPath, "remote", "add", "origin", remotePath)
	runGit(t, seedPath, "push", "-u", "origin", defaultBranch)
	runGit(t, remotePath, "symbolic-ref", "HEAD", "refs/heads/"+defaultBranch)
	return remotePath
}

// TestGTRunSingleBranchCloneFetchesRemoteBranch verifies the case that broke
// before: a single-branch clone whose configured fetch refspec does not cover
// the requested branch. gt must explicitly fetch the branch and set up
// upstream config directly, bypassing git's refspec-aware tracking checks.
func TestGTRunSingleBranchCloneFetchesRemoteBranch(t *testing.T) {
	seedPath := initRepo(t)
	binaryPath := buildBinary(t)
	remotePath := initBareWithSeed(t, seedPath)

	publisherParent := t.TempDir()
	publisherPath := filepath.Join(publisherParent, "publisher")
	runGit(t, publisherParent, "clone", remotePath, publisherPath)
	publishBranch(t, publisherPath, "remote-feature", "remote.txt", "from remote", "origin")

	// Single-branch clone restricts remote.origin.fetch to master only.
	targetParent := t.TempDir()
	targetPath := filepath.Join(targetParent, "target")
	runGit(t, targetParent, "clone", "--single-branch", "-b", "master", remotePath, targetPath)

	fetchSpec := strings.TrimSpace(string(runGit(t, targetPath, "config", "--get-all", "remote.origin.fetch")))
	if strings.Contains(fetchSpec, "remote-feature") || strings.Contains(fetchSpec, "*") {
		t.Fatalf("expected restricted fetch refspec, got %q", fetchSpec)
	}

	cmd := exec.Command(binaryPath, "-x", "true", "remote-feature")
	cmd.Dir = targetPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(targetPath, defaultWorktreeDir, "remote-feature")
	if _, err := os.Stat(filepath.Join(worktreePath, "remote.txt")); err != nil {
		t.Fatalf("expected worktree to contain remote branch file: %v", err)
	}

	// `@{upstream}` rejects refs outside the configured refspec in single-branch
	// clones, even when the underlying config is valid. Check the config keys
	// directly, which is what gt writes and what `git pull` consults.
	remote := strings.TrimSpace(string(runGit(t, targetPath, "config", "--get", "branch.remote-feature.remote")))
	merge := strings.TrimSpace(string(runGit(t, targetPath, "config", "--get", "branch.remote-feature.merge")))
	if remote != "origin" || merge != "refs/heads/remote-feature" {
		t.Fatalf("upstream config = (%q, %q), want (origin, refs/heads/remote-feature)", remote, merge)
	}
}

// TestGTRunMultiRemoteConflictErrors verifies that when the same branch lives
// on multiple remotes and no checkout.defaultRemote is set, gt refuses to
// guess and returns a helpful error.
func TestGTRunMultiRemoteConflictErrors(t *testing.T) {
	seedPath := initRepo(t)
	binaryPath := buildBinary(t)
	remoteAPath := initBareWithSeed(t, seedPath)
	remoteBPath := filepath.Join(t.TempDir(), "originB.git")
	runGit(t, seedPath, "init", "--bare", remoteBPath)
	runGit(t, seedPath, "remote", "add", "originB", remoteBPath)
	runGit(t, seedPath, "push", "originB", "master")

	publisherParent := t.TempDir()
	publisherPath := filepath.Join(publisherParent, "publisher")
	runGit(t, publisherParent, "clone", remoteAPath, publisherPath)
	runGit(t, publisherPath, "remote", "add", "originB", remoteBPath)
	publishBranch(t, publisherPath, "shared", "shared.txt", "x", "origin", "originB")

	targetParent := t.TempDir()
	targetPath := filepath.Join(targetParent, "target")
	runGit(t, targetParent, "clone", remoteAPath, targetPath)
	runGit(t, targetPath, "remote", "add", "originB", remoteBPath)
	runGit(t, targetPath, "fetch", "originB")

	cmd := exec.Command(binaryPath, "-x", "true", "shared")
	cmd.Dir = targetPath
	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected gt to error on multi-remote ambiguity, got success:\n%s", output)
	}
	if !strings.Contains(string(output), "multiple remotes") {
		t.Fatalf("expected multi-remote error message, got:\n%s", output)
	}
}

// TestGTRunDefaultRemoteWins verifies that checkout.defaultRemote disambiguates
// when the same branch lives on multiple remotes.
func TestGTRunDefaultRemoteWins(t *testing.T) {
	seedPath := initRepo(t)
	binaryPath := buildBinary(t)
	remoteAPath := initBareWithSeed(t, seedPath)
	remoteBPath := filepath.Join(t.TempDir(), "originB.git")
	runGit(t, seedPath, "init", "--bare", remoteBPath)
	runGit(t, seedPath, "remote", "add", "originB", remoteBPath)
	runGit(t, seedPath, "push", "originB", "master")

	publisherParent := t.TempDir()
	publisherPath := filepath.Join(publisherParent, "publisher")
	runGit(t, publisherParent, "clone", remoteAPath, publisherPath)
	runGit(t, publisherPath, "remote", "add", "originB", remoteBPath)
	publishBranch(t, publisherPath, "shared", "shared.txt", "x", "origin", "originB")

	targetParent := t.TempDir()
	targetPath := filepath.Join(targetParent, "target")
	runGit(t, targetParent, "clone", remoteAPath, targetPath)
	runGit(t, targetPath, "remote", "add", "originB", remoteBPath)
	runGit(t, targetPath, "fetch", "originB")
	runGit(t, targetPath, "config", "checkout.defaultRemote", "originB")

	cmd := exec.Command(binaryPath, "-x", "true", "shared")
	cmd.Dir = targetPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	remote := strings.TrimSpace(string(runGit(t, targetPath, "config", "--get", "branch.shared.remote")))
	if remote != "originB" {
		t.Fatalf("branch.shared.remote = %q, want originB", remote)
	}
}

// TestGTRunDefaultRemoteFallthrough verifies that when checkout.defaultRemote
// is set but the branch is not on that remote, gt still finds the branch on
// the other remote where it actually exists.
func TestGTRunDefaultRemoteFallthrough(t *testing.T) {
	seedPath := initRepo(t)
	binaryPath := buildBinary(t)
	remoteAPath := initBareWithSeed(t, seedPath)
	remoteBPath := filepath.Join(t.TempDir(), "originB.git")
	runGit(t, seedPath, "init", "--bare", remoteBPath)
	runGit(t, seedPath, "remote", "add", "originB", remoteBPath)
	runGit(t, seedPath, "push", "originB", "master")

	publisherParent := t.TempDir()
	publisherPath := filepath.Join(publisherParent, "publisher")
	runGit(t, publisherParent, "clone", remoteAPath, publisherPath)
	runGit(t, publisherPath, "remote", "add", "originB", remoteBPath)
	publishBranch(t, publisherPath, "b-only", "b.txt", "x", "originB")

	targetParent := t.TempDir()
	targetPath := filepath.Join(targetParent, "target")
	runGit(t, targetParent, "clone", remoteAPath, targetPath)
	runGit(t, targetPath, "remote", "add", "originB", remoteBPath)
	runGit(t, targetPath, "fetch", "originB")
	runGit(t, targetPath, "config", "checkout.defaultRemote", "origin")

	cmd := exec.Command(binaryPath, "-x", "true", "b-only")
	cmd.Dir = targetPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	remote := strings.TrimSpace(string(runGit(t, targetPath, "config", "--get", "branch.b-only.remote")))
	if remote != "originB" {
		t.Fatalf("branch.b-only.remote = %q, want originB (fallthrough)", remote)
	}
}

func TestGTCreateWorktreeFromSourceBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a source branch with extra commits
	runGit(t, repoPath, "checkout", "-b", "develop")
	addCommits(t, repoPath, 2)
	runGit(t, repoPath, "checkout", "master")

	// Create worktree from develop
	cmd := exec.Command(binaryPath, "-x", "true", "feature-from-develop", "develop")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-from-develop")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree to exist: %v", err)
	}

	// Verify the worktree has the commits from develop
	logOutput := runGit(t, worktreePath, "log", "--oneline")
	if !strings.Contains(string(logOutput), "commit 1") {
		t.Error("expected worktree to have commits from develop branch")
	}
}

func TestGTCreateWorktreeInvalidBranchName(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Try to create worktree with invalid branch name
	cmd := exec.Command(binaryPath, "-x", "true", "feature;rm -rf")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	// Should fail
	if err == nil {
		t.Fatal("expected command to fail for invalid branch name")
	}

	// Worktree should not exist
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature;rm -rf")
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should not have been created for invalid branch name")
	}

	// Output should mention invalid
	if !strings.Contains(string(output), "invalid") && !strings.Contains(string(output), "Invalid") {
		t.Logf("output: %s", output)
	}
}

func TestGTCreateWorktreeWithCustomDir(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Set up config with custom worktree dir
	customDir := filepath.Join(repoPath, "custom-worktrees")
	configDir := filepath.Join(t.TempDir(), "gt")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("create config dir: %v", err)
	}

	configContent := `{"worktree_dir": "custom-worktrees"}`
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(configContent), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := exec.Command(binaryPath, "-x", "true", "custom-feature")
	cmd.Dir = repoPath
	cmd.Env = append(os.Environ(), "XDG_CONFIG_HOME="+filepath.Dir(configDir))
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt run failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(customDir, "custom-feature")
	if _, err := os.Stat(worktreePath); err != nil {
		t.Fatalf("expected worktree in custom dir: %v", err)
	}
}

func TestGTMergeAndDeleteWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree with commits
	cmd := exec.Command(binaryPath, "-x", "true", "feature-to-merge")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add commits to the feature branch
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-to-merge")
	addCommits(t, worktreePath, 2)

	// Merge with ff-only
	cmd = exec.Command(binaryPath, "--merge", "feature-to-merge", "--ff-only")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("merge failed: %v\n%s", err, output)
	}

	// Worktree should be deleted
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should have been deleted after merge")
	}

	// Branch should be deleted
	branchOutput := runGit(t, repoPath, "branch", "--list", "feature-to-merge")
	if strings.TrimSpace(string(branchOutput)) != "" {
		t.Error("branch should have been deleted after merge")
	}
}

func TestGTMergeSquash(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree with commits
	cmd := exec.Command(binaryPath, "-x", "true", "feature-squash")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add multiple commits
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "feature-squash")
	addCommits(t, worktreePath, 3)

	// Merge with squash
	cmd = exec.Command(binaryPath, "--merge", "feature-squash", "--squash")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("squash merge failed: %v\n%s", err, output)
	}

	// Worktree and branch should be deleted
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree should have been deleted after squash merge")
	}

	// Check that the squash commit exists
	logOutput := runGit(t, repoPath, "log", "--oneline", "-1")
	if !strings.Contains(string(logOutput), "Squash merge") {
		t.Errorf("expected squash merge commit, got: %s", logOutput)
	}
}

func TestGTMergeFailsDirtyWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "dirty-feature")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	// Add commits
	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "dirty-feature")
	addCommits(t, worktreePath, 1)

	// Make the worktree dirty
	createDirtyState(t, worktreePath)

	// Try to merge - should fail
	cmd = exec.Command(binaryPath, "--merge", "dirty-feature")
	cmd.Dir = repoPath
	output, err = cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected merge to fail for dirty worktree")
	}

	if !strings.Contains(string(output), "uncommitted") {
		t.Logf("output: %s", output)
	}

	// Worktree should still exist
	if _, err := os.Stat(worktreePath); err != nil {
		t.Error("worktree should still exist after failed merge")
	}
}

func TestGTMergeFailsCurrentWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "current-feature")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "current-feature")
	addCommits(t, worktreePath, 1)

	// Try to merge from within the worktree - should fail
	cmd = exec.Command(binaryPath, "--merge", "current-feature")
	cmd.Dir = worktreePath
	output, err = cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected merge to fail when run from the worktree being merged")
	}

	if !strings.Contains(string(output), "current") {
		t.Logf("output: %s", output)
	}
}

func TestGTDeleteWorktree(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	// Create a worktree
	cmd := exec.Command(binaryPath, "-x", "true", "to-delete")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("create worktree failed: %v\n%s", err, output)
	}

	worktreePath := filepath.Join(repoPath, defaultWorktreeDir, "to-delete")

	// Delete the worktree using git directly (since gt delete requires TUI)
	runGit(t, repoPath, "worktree", "remove", worktreePath, "--force")

	// Verify it's gone
	if _, err := os.Stat(worktreePath); err == nil {
		t.Error("worktree directory should have been deleted")
	}
}

func TestGTHelpFlag(t *testing.T) {
	binaryPath := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"short flag", []string{"-h"}},
		{"long flag", []string{"--help"}},
		{"help word", []string{"help"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gt %v failed: %v\n%s", tt.args, err, output)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, "Git Worktree Manager") {
				t.Errorf("expected help output to contain 'Git Worktree Manager', got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, "USAGE:") {
				t.Errorf("expected help output to contain 'USAGE:', got:\n%s", outputStr)
			}
		})
	}
}

func TestGTVersionFlag(t *testing.T) {
	binaryPath := buildBinary(t)

	tests := []struct {
		name string
		args []string
	}{
		{"short flag", []string{"-v"}},
		{"long flag", []string{"--version"}},
		{"version word", []string{"version"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := exec.Command(binaryPath, tt.args...)
			output, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("gt %v failed: %v\n%s", tt.args, err, output)
			}

			outputStr := string(output)
			if !strings.Contains(outputStr, "gt version") {
				t.Errorf("expected version output, got:\n%s", outputStr)
			}
			if !strings.Contains(outputStr, version) {
				t.Errorf("expected version %s in output, got:\n%s", version, outputStr)
			}
		})
	}
}

func TestGTCompletionBash(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "bash")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion bash failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "_gt_completions") {
		t.Errorf("expected bash completion function, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "complete -F") {
		t.Errorf("expected complete command, got:\n%s", outputStr)
	}
}

func TestGTCompletionZsh(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "zsh")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion zsh failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "#compdef gt") {
		t.Errorf("expected zsh compdef, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "_arguments") {
		t.Errorf("expected _arguments, got:\n%s", outputStr)
	}
}

func TestGTCompletionFish(t *testing.T) {
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "completion", "fish")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("gt completion fish failed: %v\n%s", err, output)
	}

	outputStr := string(output)
	if !strings.Contains(outputStr, "Fish completion for gt") {
		t.Errorf("expected fish completion header, got:\n%s", outputStr)
	}
	if !strings.Contains(outputStr, "complete -c gt") {
		t.Errorf("expected complete command, got:\n%s", outputStr)
	}
}

func TestGTMergeRequiresBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "--merge")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected --merge without branch to fail")
	}

	if !strings.Contains(string(output), "requires") {
		t.Logf("output: %s", output)
	}
}

func TestGTMergeNonexistentBranch(t *testing.T) {
	repoPath := initRepo(t)
	binaryPath := buildBinary(t)

	cmd := exec.Command(binaryPath, "--merge", "nonexistent-branch")
	cmd.Dir = repoPath
	output, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatal("expected --merge with nonexistent branch to fail")
	}

	if !strings.Contains(string(output), "not found") {
		t.Logf("output: %s", output)
	}
}
