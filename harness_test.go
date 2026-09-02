package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-git/go-billy/v5"
	"github.com/go-git/go-billy/v5/memfs"
	billyutil "github.com/go-git/go-billy/v5/util"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// harness drives a git repo through the workflow so we can assert genver's
// output at each step of the TASK.md worked example. The repository is backed
// entirely by an in-memory filesystem, so tests never touch disk.
type harness struct {
	t   testing.TB
	g   *repo
	wt  *git.Worktree
	wfs billy.Filesystem // in-memory target for --write-to output
	n   int              // monotonic counter for unique commit content and timestamps
}

func newHarness(t testing.TB) *harness {
	return newHarnessNamed(t, "main")
}

// newHarnessNamed builds a harness whose permanent release branch is `mainName`
// ("main" or "master"), so both permanent-branch names can be exercised.
func newHarnessNamed(t testing.TB, mainName string) *harness {
	t.Helper()
	// Back both the object store and the worktree with in-memory filesystems.
	// The storer is a *filesystem.Storage (not a bare in-memory storer) so that
	// production's storage path is exercised unchanged.
	storer := filesystem.NewStorage(memfs.New(), cache.NewObjectLRUDefault())
	r, err := git.Init(storer, memfs.New())
	if err != nil {
		t.Fatalf("init in-memory repo: %v", err)
	}
	// Use the given name as the permanent branch.
	if err := r.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(mainName)),
	); err != nil {
		t.Fatalf("set HEAD to %s: %v", mainName, err)
	}
	wt, err := r.Worktree()
	if err != nil {
		t.Fatalf("worktree: %v", err)
	}
	// Route --write-to output into a dedicated in-memory filesystem so the
	// write-to tests never touch disk, mirroring the object store's memfs.
	wfs := memfs.New()
	g := &repo{r: r, out: billyFileWriter{wfs}}
	return &harness{t: t, g: g, wt: wt, wfs: wfs}
}

// billyFileWriter is a fileWriter backed by an in-memory billy filesystem, so
// --write-to output stays off disk in tests.
type billyFileWriter struct {
	fs billy.Filesystem
}

func (w billyFileWriter) WriteFile(name string, data []byte, perm os.FileMode) error {
	return billyutil.WriteFile(w.fs, name, data, perm)
}

// readWriteTo returns the contents --write-to persisted at name in the
// harness's in-memory filesystem.
func (h *harness) readWriteTo(name string) string {
	h.t.Helper()
	data, err := billyutil.ReadFile(h.wfs, name)
	if err != nil {
		h.t.Fatalf("read write-to file %q: %v", name, err)
	}
	return string(data)
}

func (h *harness) sig() *object.Signature {
	h.n++
	// Deterministic, strictly increasing timestamps keep history ordering stable.
	return &object.Signature{
		Name:  "Test",
		Email: "test@example.com",
		When:  time.Date(2020, 1, 1, 0, 0, h.n, 0, time.UTC),
	}
}

// commit makes a direct commit on the current branch with a unique tree change.
func (h *harness) commit(msg string) plumbing.Hash {
	h.t.Helper()
	fname := fmt.Sprintf("f%d.txt", h.n+1)
	// Write through the worktree's own (in-memory) filesystem.
	if err := billyutil.WriteFile(h.wt.Filesystem, fname, fmt.Appendf(nil, "content %d", h.n+1), 0o644); err != nil {
		h.t.Fatal(err)
	}
	if _, err := h.wt.Add(fname); err != nil {
		h.t.Fatal(err)
	}
	hash, err := h.wt.Commit(msg, &git.CommitOptions{Author: h.sig()})
	if err != nil {
		h.t.Fatalf("commit %q: %v", msg, err)
	}
	return hash
}

// commits makes n direct commits on the current branch, each with a unique
// message, returning the hash of the last one.
func (h *harness) commits(n int) plumbing.Hash {
	h.t.Helper()
	var last plumbing.Hash
	for range n {
		last = h.commit(fmt.Sprintf("commit %d", h.n+1))
	}
	return last
}

// checkout switches to an existing branch.
func (h *harness) checkout(branch string) {
	h.t.Helper()
	err := h.wt.Checkout(&git.CheckoutOptions{Branch: plumbing.NewBranchReferenceName(branch)})
	if err != nil {
		h.t.Fatalf("checkout %q: %v", branch, err)
	}
}

// detachHead points HEAD directly at its current commit hash, emulating the
// detached-HEAD state a CI checkout leaves behind (checkout of a bare SHA).
func (h *harness) detachHead() {
	h.t.Helper()
	head, err := h.g.r.Head()
	if err != nil {
		h.t.Fatal(err)
	}
	if err := h.g.r.Storer.SetReference(plumbing.NewHashReference(plumbing.HEAD, head.Hash())); err != nil {
		h.t.Fatalf("detach HEAD: %v", err)
	}
}

// newBranch creates and switches to a new branch from the current HEAD.
func (h *harness) newBranch(branch string) {
	h.t.Helper()
	err := h.wt.Checkout(&git.CheckoutOptions{
		Branch: plumbing.NewBranchReferenceName(branch),
		Create: true,
	})
	if err != nil {
		h.t.Fatalf("new branch %q: %v", branch, err)
	}
}

// orphanBranch creates and switches to a new branch with no parent history: an
// unborn branch whose first commit becomes an independent root, sharing no
// common ancestor with any existing branch. This lets tests build unrelated
// histories (e.g. a develop line that does not descend from main). The caller
// must make the first commit on the returned branch (via commit) to give it a
// root; until then the branch has no commits.
func (h *harness) orphanBranch(branch string) {
	h.t.Helper()
	// Point HEAD at a brand-new, unborn branch ref. go-git's worktree commit
	// with no resolvable HEAD parent produces a rootless commit.
	if err := h.g.r.Storer.SetReference(
		plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(branch)),
	); err != nil {
		h.t.Fatalf("orphan branch %q: %v", branch, err)
	}
	// Clear the worktree so the orphan root does not inherit the previous
	// branch's tree; a fresh in-memory filesystem gives it an empty starting
	// tree, and an empty index drops the previous branch's staged entries (so a
	// later Checkout does not see phantom "unstaged changes").
	h.wt.Filesystem = memfs.New()
	if err := h.g.r.Storer.SetIndex(&index.Index{Version: 2}); err != nil {
		h.t.Fatalf("orphan branch %q: reset index: %v", branch, err)
	}
}

// merge performs a non-fast-forward merge of `from` into the current branch,
// using the git-standard merge commit message.
func (h *harness) merge(from string) plumbing.Hash {
	return h.mergeMsg(from, fmt.Sprintf("Merge branch '%s'", from))
}

// mergePR performs a merge of `from` using a GitHub pull-request merge commit
// message, where the head ref is prefixed with the repo owner.
func (h *harness) mergePR(from string, prNumber int, owner string) plumbing.Hash {
	msg := fmt.Sprintf("Merge pull request #%d from %s/%s", prNumber, owner, from)
	return h.mergeMsg(from, msg)
}

// mergeRemote performs a merge of `from` using the message git writes when a
// remote-tracking branch is merged directly, e.g.
// "Merge remote-tracking branch 'origin/feature/foo' into develop".
func (h *harness) mergeRemote(from, remote string) plumbing.Hash {
	msg := fmt.Sprintf("Merge remote-tracking branch '%s/%s' into develop", remote, from)
	return h.mergeMsg(from, msg)
}

// mergeBitbucketServer performs a merge of `from` using a Bitbucket Server
// (formerly Stash / Data Center) pull-request merge commit message, whose
// subject is "Pull request #<n>: <title>" and whose body carries the
// source/target refs, e.g. "Merge in PROJECT/repo from feature/foo to develop".
func (h *harness) mergeBitbucketServer(from string, prNumber int, title, project, target string) plumbing.Hash {
	msg := fmt.Sprintf("Pull request #%d: %s\n\nMerge in %s from %s to %s",
		prNumber, title, project, from, target)
	return h.mergeMsg(from, msg)
}

// mergeMsg performs a non-fast-forward merge of `from` into the current branch
// by constructing a merge commit with two parents (go-git has no high-level
// merge), using the given commit message.
func (h *harness) mergeMsg(from, msg string) plumbing.Hash {
	h.t.Helper()
	head, err := h.g.r.Head()
	if err != nil {
		h.t.Fatal(err)
	}
	fromRef, err := h.g.r.Reference(plumbing.NewBranchReferenceName(from), true)
	if err != nil {
		h.t.Fatal(err)
	}
	fromCommit, err := h.g.r.CommitObject(fromRef.Hash())
	if err != nil {
		h.t.Fatal(err)
	}
	// Check out the merged branch's tree so the merge commit's tree matches it,
	// then commit with both parents.
	if err := h.wt.Checkout(&git.CheckoutOptions{Hash: fromCommit.Hash, Force: true}); err != nil {
		h.t.Fatal(err)
	}
	sig := h.sig()
	hash, err := h.wt.Commit(msg, &git.CommitOptions{
		Author:            sig,
		Parents:           []plumbing.Hash{head.Hash(), fromCommit.Hash},
		AllowEmptyCommits: true,
	})
	if err != nil {
		h.t.Fatalf("merge %q: %v", from, err)
	}
	// Point the current branch ref at the new merge commit and restore HEAD.
	branchName := head.Name()
	if err := h.g.r.Storer.SetReference(plumbing.NewHashReference(branchName, hash)); err != nil {
		h.t.Fatal(err)
	}
	if err := h.g.r.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branchName)); err != nil {
		h.t.Fatal(err)
	}
	if err := h.wt.Checkout(&git.CheckoutOptions{Branch: branchName, Force: true}); err != nil {
		h.t.Fatal(err)
	}
	return hash
}

// octopusMerge performs a non-fast-forward octopus merge, creating a single
// merge commit whose parents are [currentHEAD, from[0], from[1], ...]. It
// generalizes mergeMsg to 3+ parents so octopus topologies (merging several
// branches at once) can be exercised. The resulting commit's tree matches the
// first merged branch's tip. At least one `from` branch is required; with a
// single `from` the behavior matches mergeMsg's default merge message.
func (h *harness) octopusMerge(msg string, from ...string) plumbing.Hash {
	h.t.Helper()
	if len(from) == 0 {
		h.t.Fatal("octopusMerge requires at least one branch to merge")
	}
	head, err := h.g.r.Head()
	if err != nil {
		h.t.Fatal(err)
	}
	parents := []plumbing.Hash{head.Hash()}
	var firstCommit *object.Commit
	for _, br := range from {
		ref, rerr := h.g.r.Reference(plumbing.NewBranchReferenceName(br), true)
		if rerr != nil {
			h.t.Fatal(rerr)
		}
		commit, cerr := h.g.r.CommitObject(ref.Hash())
		if cerr != nil {
			h.t.Fatal(cerr)
		}
		if firstCommit == nil {
			firstCommit = commit
		}
		parents = append(parents, commit.Hash)
	}
	// Check out the first merged branch's tree so the merge commit's tree
	// matches it, then commit with all parents.
	if err := h.wt.Checkout(&git.CheckoutOptions{Hash: firstCommit.Hash, Force: true}); err != nil {
		h.t.Fatal(err)
	}
	hash, err := h.wt.Commit(msg, &git.CommitOptions{
		Author:            h.sig(),
		Parents:           parents,
		AllowEmptyCommits: true,
	})
	if err != nil {
		h.t.Fatalf("octopus merge %v: %v", from, err)
	}
	branchName := head.Name()
	if err := h.g.r.Storer.SetReference(plumbing.NewHashReference(branchName, hash)); err != nil {
		h.t.Fatal(err)
	}
	if err := h.g.r.Storer.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, branchName)); err != nil {
		h.t.Fatal(err)
	}
	if err := h.wt.Checkout(&git.CheckoutOptions{Branch: branchName, Force: true}); err != nil {
		h.t.Fatal(err)
	}
	return hash
}

func (h *harness) tag(name string, hash plumbing.Hash) {
	h.t.Helper()
	if _, err := h.g.r.CreateTag(name, hash, nil); err != nil {
		h.t.Fatalf("tag %q: %v", name, err)
	}
}

// annotatedTag creates an annotated tag (a tag object with a tagger and
// message) pointing at hash, as opposed to the lightweight tag created by tag.
func (h *harness) annotatedTag(name string, hash plumbing.Hash) {
	h.t.Helper()
	if _, err := h.g.r.CreateTag(name, hash, &git.CreateTagOptions{
		Tagger:  h.sig(),
		Message: "release " + name,
	}); err != nil {
		h.t.Fatalf("annotated tag %q: %v", name, err)
	}
}

// deleteBranch removes a branch ref, emulating short-lived branch cleanup.
func (h *harness) deleteBranch(branch string) {
	h.t.Helper()
	if err := h.g.r.Storer.RemoveReference(plumbing.NewBranchReferenceName(branch)); err != nil {
		h.t.Fatalf("delete branch %q: %v", branch, err)
	}
}

// addRemote registers a remote with the given name and a dummy URL, so that
// remote-tracking refs created via remoteRef can be attributed to a configured
// remote (branchCommit's fallback splits refs/remotes/<remote>/<branch> using
// the configured remote names).
func (h *harness) addRemote(name string) {
	h.t.Helper()
	if _, err := h.g.r.CreateRemote(&config.RemoteConfig{
		Name: name,
		URLs: []string{"https://example.invalid/" + name + ".git"},
	}); err != nil {
		h.t.Fatalf("add remote %q: %v", name, err)
	}
}

// remoteRef creates a remote-tracking ref refs/remotes/<remote>/<branch>
// pointing at hash, emulating the state after `git fetch` in a fresh clone or
// CI checkout where only the checked-out branch has a local head.
func (h *harness) remoteRef(remote, branch string, hash plumbing.Hash) {
	h.t.Helper()
	if err := h.g.r.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewRemoteReferenceName(remote, branch), hash),
	); err != nil {
		h.t.Fatalf("set remote ref %s/%s: %v", remote, branch, err)
	}
}

// setUpstream records branch.<branch>.remote (and merge) in the repo config, as
// `git branch --set-upstream-to` would, so the branch has a configured upstream
// remote for reference-branch resolution.
func (h *harness) setUpstream(branch, remote string) {
	h.t.Helper()
	cfg, err := h.g.r.Config()
	if err != nil {
		h.t.Fatalf("read config: %v", err)
	}
	if cfg.Branches == nil {
		cfg.Branches = map[string]*config.Branch{}
	}
	cfg.Branches[branch] = &config.Branch{
		Name:   branch,
		Remote: remote,
		Merge:  plumbing.NewBranchReferenceName(branch),
	}
	if err := h.g.r.SetConfig(cfg); err != nil {
		h.t.Fatalf("set config: %v", err)
	}
}

// moveToRemote converts a local branch into a remote-only tracking ref: it
// records refs/remotes/<remote>/<branch> at the branch's current tip and then
// deletes the local head. This reproduces a fresh clone / CI checkout where only
// the checked-out branch has a local head and every reference branch lives under
// refs/remotes. The branch must not be the currently checked-out branch.
func (h *harness) moveToRemote(branch, remote string) {
	h.t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewBranchReferenceName(branch), true)
	if err != nil {
		h.t.Fatalf("resolve local branch %q: %v", branch, err)
	}
	h.remoteRef(remote, branch, ref.Hash())
	h.deleteBranch(branch)
}

// setLocalBranch points a local branch ref (refs/heads/<branch>) at hash,
// creating or moving it. Used to place a local reference branch at a specific
// commit that diverges from its remote-tracking counterpart. The branch must
// not be the currently checked-out branch.
func (h *harness) setLocalBranch(branch string, hash plumbing.Hash) {
	h.t.Helper()
	if err := h.g.r.Storer.SetReference(
		plumbing.NewHashReference(plumbing.NewBranchReferenceName(branch), hash),
	); err != nil {
		h.t.Fatalf("set local branch %s: %v", branch, err)
	}
}

// want asserts genver's output for the current branch equals expect.
func (h *harness) want(expect string) {
	h.t.Helper()
	if got := h.version(); got != expect {
		branch, _ := h.g.headBranch()
		h.t.Fatalf("on branch %q: got %q, want %q", branch, got, expect)
	}
}

// version computes and returns genver's output for the current branch.
func (h *harness) version() string {
	h.t.Helper()
	branch, err := h.g.headBranch()
	if err != nil {
		h.t.Fatalf("headBranch: %v", err)
	}
	h.g.setPreferredRemoteFor(branch)
	head, err := h.g.headCommit()
	if err != nil {
		h.t.Fatalf("headCommit: %v", err)
	}
	calc, err := newCalculator(h.g)
	if err != nil {
		h.t.Fatalf("newCalculator: %v", err)
	}
	res, err := calc.Calculate(branch, head)
	if err != nil {
		h.t.Fatalf("Calculate on %q: %v", branch, err)
	}
	got, err := res.version()
	if err != nil {
		h.t.Fatalf("version: %v", err)
	}
	return got
}

// release cuts a release: from develop, merge into main and tag it, then return
// to develop. Mirrors the workflow's "merge develop into main, tag" step.
func (h *harness) release(tag string) {
	h.t.Helper()
	h.checkout("main")
	mg := h.merge("develop")
	h.tag(tag, mg)
	h.checkout("develop")
}

// backMerge merges main back into the current branch (develop), using the
// git-standard merge message. This is the cross-merge that broke a naive
// "stop at the boundary commit" counter: the merge makes already-released
// commits reachable from develop via a path that bypasses the current
// section's boundary commit.
func (h *harness) backMerge() plumbing.Hash {
	h.t.Helper()
	return h.merge("main")
}

// runCapture runs the CLI against the harness and returns trimmed stdout and
// the run error.
func runCapture(t *testing.T, h *harness, args ...string) (string, error) {
	stdout, _, err := runCaptureAll(t, h, args...)
	return stdout, err
}

// runCaptureAll runs the CLI against the harness's in-memory repository and
// returns trimmed stdout, trimmed stderr, and the run error. The repo is
// injected directly, so nothing is read from or written to disk.
func runCaptureAll(t *testing.T, h *harness, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	rerr := runWithRepo(h.g, args, &stdout, &stderr)
	return strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()), rerr
}

// mustHead returns the current HEAD commit hash, failing the test on error.
func mustHead(t testing.TB, h *harness) plumbing.Hash {
	t.Helper()
	head, err := h.g.headCommit()
	if err != nil {
		t.Fatalf("headCommit: %v", err)
	}
	return head.Hash
}

// localTagHash returns the hash the named tag points at in the harness's
// repository, or the zero hash if the tag does not exist.
func localTagHash(t *testing.T, h *harness, tag string) plumbing.Hash {
	t.Helper()
	ref, err := h.g.r.Reference(plumbing.NewTagReferenceName(tag), false)
	if err != nil {
		return plumbing.ZeroHash
	}
	return ref.Hash()
}

// writeToExists reports whether --write-to persisted anything (file or dir) at
// name in the harness's in-memory filesystem. Unlike readWriteTo it does not
// fail the test when the entry is absent, so it can assert non-existence.
func writeToExists(t *testing.T, h *harness, name string) bool {
	t.Helper()
	if _, err := h.wfs.Stat(name); err == nil {
		return true
	}
	return false
}

// writeToFileCount returns how many regular files --write-to persisted directly
// in dir (subdirectories are counted as one entry, not recursed).
func writeToFileCount(t *testing.T, h *harness, dir string) int {
	t.Helper()
	entries, err := h.wfs.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}
