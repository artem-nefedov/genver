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
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	commitgraph "github.com/go-git/go-git/v5/plumbing/format/commitgraph/v2"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// harness drives a git repo through the workflow so we can assert givi's
// output at each step of the TASK.md worked example. The repository is backed
// entirely by an in-memory filesystem, so tests never touch disk (which also
// lets them write a real commit-graph into the object store; see
// writeCommitGraph).
type harness struct {
	t   *testing.T
	g   *repo
	wt  *git.Worktree
	wfs billy.Filesystem // in-memory target for --write-to output
	n   int              // monotonic counter for unique commit content and timestamps
}

func newHarness(t *testing.T) *harness {
	return newHarnessNamed(t, "main")
}

// newHarnessNamed builds a harness whose permanent release branch is `mainName`
// ("main" or "master"), so both permanent-branch names can be exercised.
func newHarnessNamed(t *testing.T, mainName string) *harness {
	t.Helper()
	// Back both the object store and the worktree with in-memory filesystems.
	// The storer is a *filesystem.Storage (not a bare in-memory storer) so that
	// production's commit-graph detection path is exercised unchanged.
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

// want asserts givi's output for the current branch equals expect.
func (h *harness) want(expect string) {
	h.t.Helper()
	branch, err := h.g.headBranch()
	if err != nil {
		h.t.Fatalf("headBranch: %v", err)
	}
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
	if got != expect {
		h.t.Fatalf("on branch %q: got %q, want %q", branch, got, expect)
	}
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

// writeCommitGraph builds a commit-graph file covering the given commit hashes
// and writes it to objects/info/commit-graph inside the repository's own
// (billy) filesystem. It uses go-git's own commit-graph encoder, so no external
// git binary and no real disk are involved: an in-memory repo gets a real,
// loadable commit-graph. Passing a subset of the repo's commits produces a
// deliberately *stale* graph, exercising go-git's per-commit fallback to the
// object store for the commits it does not cover.
func writeCommitGraph(t *testing.T, g *repo, hashes []plumbing.Hash) {
	t.Helper()

	fss, ok := g.r.Storer.(*filesystem.Storage)
	if !ok {
		t.Fatalf("repo storer is %T, not *filesystem.Storage; cannot host a commit-graph", g.r.Storer)
	}

	mem := commitgraph.NewMemoryIndex()
	// Generation numbers must respect ancestry (a commit's generation exceeds
	// all its parents'). Assign them by walking parents-before-children.
	gen := map[plumbing.Hash]uint64{}
	var assign func(h plumbing.Hash) uint64
	assign = func(h plumbing.Hash) uint64 {
		if v, ok := gen[h]; ok {
			return v
		}
		commit, err := g.r.CommitObject(h)
		if err != nil {
			t.Fatalf("load commit %s: %v", h, err)
		}
		var maxParent uint64
		for _, ph := range commit.ParentHashes {
			if pg := assign(ph); pg > maxParent {
				maxParent = pg
			}
		}
		v := maxParent + 1
		gen[h] = v
		return v
	}

	for _, h := range hashes {
		commit, err := g.r.CommitObject(h)
		if err != nil {
			t.Fatalf("load commit %s: %v", h, err)
		}
		mem.Add(h, &commitgraph.CommitData{
			TreeHash:     commit.TreeHash,
			ParentHashes: append([]plumbing.Hash(nil), commit.ParentHashes...),
			Generation:   assign(h),
			When:         commit.Committer.When,
		})
	}

	var buf bytes.Buffer
	if err := commitgraph.NewEncoder(&buf).Encode(mem); err != nil {
		t.Fatalf("encode commit-graph: %v", err)
	}

	fs := fss.Filesystem()
	if err := fs.MkdirAll("objects/info", 0o755); err != nil {
		t.Fatalf("mkdir objects/info: %v", err)
	}
	f, err := fs.Create("objects/info/commit-graph")
	if err != nil {
		t.Fatalf("create commit-graph file: %v", err)
	}
	if _, err := f.Write(buf.Bytes()); err != nil {
		t.Fatalf("write commit-graph: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close commit-graph: %v", err)
	}

	// Sanity check: the just-written graph must be openable via the same path
	// production code uses.
	if _, err := commitgraph.OpenChainOrFileIndex(fs); err != nil {
		t.Fatalf("commit-graph not loadable after write: %v", err)
	}
}

// allCommitHashes returns every commit hash in the repository.
func allCommitHashes(t *testing.T, g *repo) []plumbing.Hash {
	t.Helper()
	iter, err := g.r.CommitObjects()
	if err != nil {
		t.Fatalf("CommitObjects: %v", err)
	}
	defer iter.Close()
	var out []plumbing.Hash
	err = iter.ForEach(func(c *object.Commit) error {
		out = append(out, c.Hash)
		return nil
	})
	if err != nil {
		t.Fatalf("iterate commits: %v", err)
	}
	return out
}

// versionThroughGraph computes the version for the repo's current branch with
// the commit-graph fast path either enabled or disabled, returning the string.
// When useGraph is true it also asserts the trace confirms the commit-graph was
// actually used — otherwise the parity comparison would be vacuous (two runs of
// the same object-store path).
func versionThroughGraph(t *testing.T, g *repo, useGraph bool) string {
	t.Helper()
	branch, err := g.headBranch()
	if err != nil {
		t.Fatalf("headBranch: %v", err)
	}
	head, err := g.headCommit()
	if err != nil {
		t.Fatalf("headCommit: %v", err)
	}
	var trace bytes.Buffer
	calc, err := newCalculatorOpts(g, &trace, useGraph)
	if err != nil {
		t.Fatalf("newCalculatorOpts(useGraph=%v): %v", useGraph, err)
	}
	if useGraph && !bytes.Contains(trace.Bytes(), []byte("using commit-graph")) {
		t.Fatalf("expected commit-graph fast path to be active, but trace says otherwise:\n%s", trace.String())
	}
	res, err := calc.Calculate(branch, head)
	if err != nil {
		t.Fatalf("Calculate(useGraph=%v) on %q: %v", useGraph, branch, err)
	}
	v, err := res.version()
	if err != nil {
		t.Fatalf("version(useGraph=%v): %v", useGraph, err)
	}
	return v
}

// assertGraphParity asserts that, on the current branch, givi computes the same
// version whether or not the commit-graph fast path is used.
func (h *harness) assertGraphParity() {
	h.t.Helper()
	withGraph := versionThroughGraph(h.t, h.g, true)
	withoutGraph := versionThroughGraph(h.t, h.g, false)
	if withGraph != withoutGraph {
		branch, _ := h.g.headBranch()
		h.t.Fatalf("commit-graph parity broken on %q: with graph %q, without graph %q",
			branch, withGraph, withoutGraph)
	}
}

// wantBothPaths writes a fresh, complete commit-graph over the repository's
// entire current history, then asserts that givi produces expect on the current
// branch both with the commit-graph fast path and with the object-store slow
// path. Rewriting the graph on each call keeps it complete even as later
// assertions add commits, so both paths are genuinely exercised every time.
func (h *harness) wantBothPaths(expect string) {
	h.t.Helper()
	writeCommitGraph(h.t, h.g, allCommitHashes(h.t, h.g))
	if got := versionThroughGraph(h.t, h.g, true); got != expect {
		branch, _ := h.g.headBranch()
		h.t.Fatalf("with commit-graph on %q: got %q, want %q", branch, got, expect)
	}
	if got := versionThroughGraph(h.t, h.g, false); got != expect {
		branch, _ := h.g.headBranch()
		h.t.Fatalf("without commit-graph on %q: got %q, want %q", branch, got, expect)
	}
}
