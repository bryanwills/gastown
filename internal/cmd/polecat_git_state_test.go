package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/steveyegge/gastown/internal/git"
)

func TestGetGitStatePushedSplitRemoteBranchIsClean(t *testing.T) {
	localDir := initCmdTestSplitRemoteRepo(t)
	g := git.NewGit(localDir)
	branch := "polecat/git-state-split"

	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "split-state.go"), []byte("package splitstate\n"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := g.Add("split-state.go"); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := g.Commit("split state work"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := g.Push("origin", branch, false); err != nil {
		t.Fatalf("Push: %v", err)
	}

	state, err := getGitState(localDir)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !state.Clean || state.UnpushedCommits != 0 || !state.HasBranchWork {
		t.Fatalf("getGitState = clean %v, unpushed %d, branch work %v; want clean pushed branch work", state.Clean, state.UnpushedCommits, state.HasBranchWork)
	}
}

func TestGetGitStateLocalCommitAheadOfPushTargetNeedsRecovery(t *testing.T) {
	localDir := initCmdTestSplitRemoteRepo(t)
	g := git.NewGit(localDir)
	branch := "polecat/git-state-local-ahead"

	if err := g.CreateBranch(branch); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := g.Checkout(branch); err != nil {
		t.Fatalf("Checkout: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "state-ahead.go"), []byte("package stateahead\n"), 0644); err != nil {
		t.Fatalf("write v1: %v", err)
	}
	if err := g.Add("state-ahead.go"); err != nil {
		t.Fatalf("Add v1: %v", err)
	}
	if err := g.Commit("state ahead v1"); err != nil {
		t.Fatalf("Commit v1: %v", err)
	}
	if err := g.Push("origin", branch, false); err != nil {
		t.Fatalf("Push v1: %v", err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "state-ahead.go"), []byte("package stateahead\nconst V = 2\n"), 0644); err != nil {
		t.Fatalf("write v2: %v", err)
	}
	if err := g.Add("state-ahead.go"); err != nil {
		t.Fatalf("Add v2: %v", err)
	}
	if err := g.Commit("state ahead v2"); err != nil {
		t.Fatalf("Commit v2: %v", err)
	}

	state, err := getGitState(localDir)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if state.Clean || state.UnpushedCommits != 1 {
		t.Fatalf("getGitState = clean %v, unpushed %d; want one local commit at risk", state.Clean, state.UnpushedCommits)
	}
}

func TestGetGitStateIgnoresGeneratedOpenCodeHook(t *testing.T) {
	localDir := initCmdTestSplitRemoteRepo(t)
	g := git.NewGit(localDir)
	pluginPath := filepath.Join(localDir, ".opencode", "plugins", "gastown.js")

	if err := os.MkdirAll(filepath.Dir(pluginPath), 0755); err != nil {
		t.Fatalf("mkdir plugin dir: %v", err)
	}
	if err := os.WriteFile(pluginPath, []byte("module.exports = {}\n"), 0644); err != nil {
		t.Fatalf("write plugin v1: %v", err)
	}
	if err := g.Add(".opencode/plugins/gastown.js"); err != nil {
		t.Fatalf("Add plugin: %v", err)
	}
	if err := g.Commit("track opencode hook"); err != nil {
		t.Fatalf("Commit plugin: %v", err)
	}
	if err := g.Push("origin", "HEAD", false); err != nil {
		t.Fatalf("Push plugin: %v", err)
	}
	if err := os.WriteFile(pluginPath, []byte("module.exports = { generated: true }\n"), 0644); err != nil {
		t.Fatalf("write generated plugin: %v", err)
	}

	state, err := getGitState(localDir)
	if err != nil {
		t.Fatalf("getGitState: %v", err)
	}
	if !state.Clean || len(state.UncommittedFiles) != 0 {
		t.Fatalf("getGitState = clean %v, uncommitted %v; want generated opencode hook ignored", state.Clean, state.UncommittedFiles)
	}
}

func initCmdTestSplitRemoteRepo(t *testing.T) string {
	t.Helper()
	tmp := t.TempDir()
	upstream := filepath.Join(tmp, "upstream.git")
	fork := filepath.Join(tmp, "fork.git")
	localDir := filepath.Join(tmp, "local")

	for _, bare := range []string{upstream, fork} {
		runCmdGit(t, tmp, "init", "--bare", bare)
	}
	if err := os.MkdirAll(localDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	runCmdGit(t, localDir, "init")
	runCmdGit(t, localDir, "config", "user.email", "test@test.com")
	runCmdGit(t, localDir, "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(localDir, "README.md"), []byte("# Test\n"), 0644); err != nil {
		t.Fatalf("write readme: %v", err)
	}
	runCmdGit(t, localDir, "add", ".")
	runCmdGit(t, localDir, "commit", "-m", "initial")
	runCmdGit(t, localDir, "remote", "add", "origin", upstream)
	runCmdGit(t, localDir, "push", "origin", "HEAD")
	runCmdGit(t, localDir, "push", fork, "HEAD")
	if err := git.NewGit(localDir).ConfigurePushURL("origin", fork); err != nil {
		t.Fatalf("ConfigurePushURL: %v", err)
	}
	return localDir
}

func runCmdGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}
