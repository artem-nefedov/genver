package main

import (
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/client"
	"github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"
)

// The --push-tag-to tests exercise a real push end to end, but entirely in
// memory: pushes go over go-git's in-process server transport (installed for
// the "file" scheme in TestMain) into in-memory storers, so no git binary is
// spawned and nothing touches disk. Production still uses the pure-Go http/ssh
// transports; only these tests swap the (subprocess-based) file transport for
// the in-process one.

// memLoader is a concurrency-safe server.Loader mapping an endpoint string to
// the in-memory storer backing that "remote". Tests run in parallel and share
// the single installed transport, so both the registration writes and the
// server's lookup reads must be guarded.
type memLoader struct {
	mu    sync.Mutex
	repos map[string]storer.Storer
}

func (l *memLoader) add(key string, s storer.Storer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.repos[key] = s
}

func (l *memLoader) Load(ep *transport.Endpoint) (storer.Storer, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	s, ok := l.repos[ep.String()]
	if !ok {
		return nil, transport.ErrRepositoryNotFound
	}
	return s, nil
}

// testLoader backs every in-memory remote used by the push tests.
var testLoader = &memLoader{repos: map[string]storer.Storer{}}

// TestMain installs the in-process server transport for the "file" scheme so
// pushes to file:// URLs are served from testLoader without a subprocess.
func TestMain(m *testing.M) {
	client.Protocols["file"] = server.NewServer(testLoader)
	os.Exit(m.Run())
}

// newMemRemote registers a fresh in-memory storer as a remote and returns the
// file:// URL addressing it (unique per test) plus the storer for inspection.
// A file:// scheme URL (rather than a bare path) keeps go-git on the pure
// send-pack path instead of its local-filesystem osfs optimization.
func newMemRemote(t *testing.T) (string, storer.Storer) {
	t.Helper()
	// t.Name() is unique per test/subtest across a run; "/" from subtests is
	// fine inside a URL path.
	url := "file:///" + t.Name() + ".git"
	ep, err := transport.NewEndpoint(url)
	if err != nil {
		t.Fatalf("endpoint %q: %v", url, err)
	}
	st := memory.NewStorage()
	testLoader.add(ep.String(), st)
	return url, st
}

// memTagHash returns the hash the named tag points at in an in-memory remote
// storer, or the zero hash if absent. Annotated tags are dereferenced to their
// target commit.
func memTagHash(t *testing.T, st storer.Storer, tag string) plumbing.Hash {
	t.Helper()
	ref, err := st.Reference(plumbing.NewTagReferenceName(tag))
	if err != nil {
		return plumbing.ZeroHash
	}
	if tobj, err := object.GetTag(st, ref.Hash()); err == nil {
		return tobj.Target
	}
	return ref.Hash()
}

// TestPushTagToNamedRemote pushes the freshly created tag to a configured
// remote and asserts that ONLY that tag arrives (no branches, no other tags).
func TestPushTagToNamedRemote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root") // main = 0.1.0
	url, st := newMemRemote(t)

	if _, err := h.g.r.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// An extra tag on HEAD that must NOT be pushed, proving the push is
	// restricted to the computed tag only.
	h.tag("other-tag", head)

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin")
	if err != nil || out != "0.1.0" {
		t.Fatalf("--tag-main --push-tag-to origin: out=%q err=%v", out, err)
	}

	if got := memTagHash(t, st, "0.1.0"); got != head {
		t.Errorf("tag 0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
	// Nothing else should have been pushed.
	if got := memTagHash(t, st, "other-tag"); got != plumbing.ZeroHash {
		t.Errorf("other-tag was pushed (%s) but only the computed tag should be", got)
	}
	if _, err := st.Reference(plumbing.NewBranchReferenceName("main")); err == nil {
		t.Errorf("branch main was pushed to the remote, but only the tag should be")
	}
}

// TestPushTagToURL pushes to a remote given as a URL, with no remote configured
// in the repo.
func TestPushTagToURL(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root")
	url, st := newMemRemote(t)

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", url)
	if err != nil || out != "0.1.0" {
		t.Fatalf("--push-tag-to <url>: out=%q err=%v", out, err)
	}
	if got := memTagHash(t, st, "0.1.0"); got != head {
		t.Errorf("tag 0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
}

// TestPushTagToPreexistingTag pushes when the tag already exists on HEAD (genver
// did not create it this run), in both lightweight and annotated form.
func TestPushTagToPreexistingTag(t *testing.T) {
	t.Parallel()
	for _, annotated := range []bool{false, true} {
		name := "lightweight"
		if annotated {
			name = "annotated"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			head := h.commit("root")
			url, st := newMemRemote(t)

			// Pre-create the tag 0.1.0 on HEAD before genver runs.
			if annotated {
				h.annotatedTag("0.1.0", head)
			} else {
				h.tag("0.1.0", head)
			}

			out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", url)
			if err != nil || out != "0.1.0" {
				t.Fatalf("push preexisting %s tag: out=%q err=%v", name, out, err)
			}
			if got := memTagHash(t, st, "0.1.0"); got != head {
				t.Errorf("tag on remote points at %s, want HEAD %s", got, head)
			}
		})
	}
}

// TestPushTagToTagOnOtherCommit errors when a tag with the computed name exists
// but points at a different commit than HEAD. The clash is built by tagging an
// off-chain commit with the name genver computes for main's HEAD (0.1.0): the tag
// exists but does not mark HEAD, so genver must refuse to push it.
func TestPushTagToTagOnOtherCommit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root") // main = 0.1.0
	url, st := newMemRemote(t)

	// Commit on a side branch and tag THAT commit "0.1.0", then return to main.
	// The tag is not on main's first-parent chain, so it does not influence the
	// computed version — main still computes 0.1.0 — but the tag now points at
	// the side commit, not HEAD.
	h.newBranch("side")
	side := h.commit("side-commit")
	h.tag("0.1.0", side)
	h.checkout("main")

	out, stderr, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", url)
	if err == nil {
		t.Fatalf("expected error pushing a tag not on HEAD, got out=%q stderr=%q", out, stderr)
	}
	if !strings.Contains(err.Error(), "not HEAD") {
		t.Errorf("unexpected error: %v", err)
	}
	// The tag existed on the side commit; the remote must be untouched.
	if got := memTagHash(t, st, "0.1.0"); got != plumbing.ZeroHash {
		t.Errorf("remote tag was pushed despite mismatch: %s", got)
	}
}

// TestPushTagToMissingNamedRemote errors when the argument is a bare name that
// is not a configured remote.
func TestPushTagToMissingNamedRemote(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	_, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "nonexistent")
	if err == nil {
		t.Fatal("expected error for missing remote name")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPushTagToRequiresTagMain asserts --push-tag-to without --tag-main is a
// usage error (not a silent no-op), and that nothing is tagged as a result.
func TestPushTagToRequiresTagMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")

	_, _, err := runCaptureAll(t, h, "--push-tag-to", "origin")
	if err == nil {
		t.Fatal("expected error for --push-tag-to without --tag-main")
	}
	if !strings.Contains(err.Error(), "requires --tag-main") {
		t.Errorf("unexpected error: %v", err)
	}
	// No tag should have been created.
	if _, err := h.g.r.Reference(plumbing.NewTagReferenceName("0.1.0"), false); err == nil {
		t.Error("tag 0.1.0 was created despite the usage error")
	}
}

// TestPushTagToIgnoredOnNonMain asserts --push-tag-to is ignored on a non-main
// branch, even with --tag-main.
func TestPushTagToIgnoredOnNonMain(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")

	_, stderr, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin", "--debug")
	if err != nil {
		t.Fatalf("--push-tag-to on develop should be ignored: err=%v", err)
	}
	if !strings.Contains(stderr, `push-tag-to: ignored on non-main branch "develop"`) {
		t.Errorf("expected ignored-on-non-main trace, got:\n%s", stderr)
	}
}

// TestPushTagToEqualsSyntax verifies the --push-tag-to=arg spelling is accepted
// (Go's flag package handles it; this guards against regressions in wiring).
func TestPushTagToEqualsSyntax(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root")
	url, st := newMemRemote(t)

	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to="+url)
	if err != nil || out != "0.1.0" {
		t.Fatalf("--push-tag-to=<url>: out=%q err=%v", out, err)
	}
	if got := memTagHash(t, st, "0.1.0"); got != head {
		t.Errorf("tag 0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
}

// TestPushTagAlreadyUpToDate pushes the same tag twice: the second push finds
// the remote already has the tag and go-git returns its "already up-to-date"
// sentinel, which normalizePushErr folds into success. genver must report no
// error and the remote tag must still mark HEAD.
func TestPushTagAlreadyUpToDate(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	head := h.commit("root")
	url, st := newMemRemote(t)

	if _, err := h.g.r.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{url},
	}); err != nil {
		t.Fatalf("create remote: %v", err)
	}

	// First push seeds the remote with the tag.
	if out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin"); err != nil || out != "0.1.0" {
		t.Fatalf("first push: out=%q err=%v", out, err)
	}
	// Second push: the remote already has the tag -> NoErrAlreadyUpToDate, which
	// must be folded into success.
	out, _, err := runCaptureAll(t, h, "--tag-main", "--push-tag-to", "origin")
	if err != nil || out != "0.1.0" {
		t.Fatalf("second (already-up-to-date) push: out=%q err=%v", out, err)
	}
	if got := memTagHash(t, st, "0.1.0"); got != head {
		t.Errorf("tag 0.1.0 on remote points at %s, want HEAD %s", got, head)
	}
}

// TestLooksLikeURL pins the classification of a --push-tag-to argument as either
// a remote URL/path (pushed to directly via an anonymous remote) or a bare
// remote name (looked up in the repo config). It mirrors TestRedactURL's style.
func TestLooksLikeURL(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"https://github.com/o/r.git": true,  // scheme
		"ssh://git@host/o/r.git":     true,  // scheme
		"git://host/o/r.git":         true,  // scheme
		"file:///tmp/r.git":          true,  // scheme
		"/abs/path/repo.git":         true,  // absolute path
		"./rel/path/repo.git":        true,  // relative path
		"../up/repo.git":             true,  // relative path
		"git@github.com:o/r.git":     true,  // scp-like (has "@" and ":")
		"origin":                     false, // bare remote name
		"upstream":                   false, // bare remote name
		"my-remote":                  false, // bare remote name
	}
	for in, want := range cases {
		if got := looksLikeURL(in); got != want {
			t.Errorf("looksLikeURL(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestRedactURL checks that credentials embedded in a push URL are stripped for
// logs/errors, while non-URL remotes and credential-free URLs pass through
// unchanged.
func TestRedactURL(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"https://x-access-token:ghs_secret@github.com/o/r.git": "https://redacted@github.com/o/r.git",
		"https://user:pw@example.com/r.git":                    "https://redacted@example.com/r.git",
		"https://github.com/o/r.git":                           "https://github.com/o/r.git",
		"origin":                                               "origin",
		"git@github.com:o/r.git":                               "git@github.com:o/r.git",
		"/tmp/local/repo.git":                                  "/tmp/local/repo.git",
	}
	for in, want := range cases {
		if got := redactURL(in); got != want {
			t.Errorf("redactURL(%q) = %q, want %q", in, got, want)
		}
	}
	// The secret itself must never survive redaction.
	if got := redactURL("https://x-access-token:ghs_secret@github.com/o/r.git"); strings.Contains(got, "ghs_secret") {
		t.Errorf("token leaked through redaction: %q", got)
	}
}
