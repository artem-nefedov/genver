package main

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/cache"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/storage/filesystem"
)

// fileWriter persists --write-to output. Production uses osFileWriter; tests
// inject an in-memory implementation so nothing touches disk.
type fileWriter interface {
	WriteFile(name string, data []byte, perm os.FileMode) error
}

// osFileWriter is the production fileWriter, backed by the real filesystem.
type osFileWriter struct{}

func (osFileWriter) WriteFile(name string, data []byte, perm os.FileMode) error {
	// --write-to paths may be templated and include directories (e.g.
	// "versions/1.2.3.txt"), so create any missing parents first. This mirrors
	// the billy-backed writer used in tests.
	if dir := filepath.Dir(name); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(name, data, perm)
}

// repo wraps a go-git repository with the helpers givi needs. All operations
// are local; nothing here touches the network.
type repo struct {
	r     *git.Repository
	out   fileWriter          // persists --write-to output
	store *filesystem.Storage // non-nil when descriptors are kept open (needs Close)
}

// Close releases any resources held by the repo. When the storage keeps file
// descriptors open (KeepDescriptors), this closes the cached packfile handles.
// It is safe to call on a repo without a tuned store (a no-op).
func (g *repo) Close() error {
	if g.store != nil {
		return g.store.Close()
	}
	return nil
}

// writeOutput writes data to name through the repo's fileWriter.
func (g *repo) writeOutput(name string, data []byte, perm os.FileMode) error {
	return g.out.WriteFile(name, data, perm)
}

func openRepo(path string) (*repo, error) {
	// First open normally so go-git performs its .git detection (including
	// worktree/submodule ".git" files and DetectDotGit walking up parents).
	base, err := git.PlainOpenWithOptions(path, &git.PlainOpenOptions{DetectDotGit: true})
	if err != nil {
		return nil, fmt.Errorf("open git repository: %w", err)
	}

	// go-git's default storage reopens (and closes) the packfile on every single
	// object read, so decoding thousands of commits/tags spends most of its
	// wall-clock time in open/close syscalls rather than in useful work. Rebuild
	// the storage over the already-detected .git filesystem with descriptors kept
	// open and a large object cache, so the pack is opened once and reused. This
	// is the single biggest lever on read performance.
	if fss, ok := base.Storer.(*filesystem.Storage); ok {
		opts := filesystem.Options{
			ExclusiveAccess: true, // givi never mutates the repo while reading
			KeepDescriptors: true, // reuse pack file descriptors across reads
		}
		store := filesystem.NewStorageWithOptions(fss.Filesystem(), cache.NewObjectLRU(256*cache.MiByte), opts)
		r, err := git.Open(store, nil)
		if err == nil {
			return &repo{r: r, out: osFileWriter{}, store: store}, nil
		}
		// If reopening with tuned options fails for any reason, fall back to the
		// already-open repository rather than failing outright.
	}
	return &repo{r: base, out: osFileWriter{}}, nil
}

// headBranch returns the short name of the branch HEAD points to. It errors on
// a detached HEAD, since the workflow is defined in terms of branches.
func (g *repo) headBranch() (string, error) {
	h, err := g.r.Head()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	if !h.Name().IsBranch() {
		return "", fmt.Errorf("HEAD is detached; givi requires a checked-out branch")
	}
	return h.Name().Short(), nil
}

// resolveBranch determines the branch name givi should compute a version for,
// reconciling the optional --branch override with HEAD's actual state:
//
//   - Attached HEAD, no override: the checked-out branch (as headBranch).
//   - Attached HEAD, override given: the checked-out branch, but only if it
//     matches the override; a mismatch is an error (guards against pointing
//     --branch at the wrong branch, e.g. in CI).
//   - Detached HEAD, override given: the override (this is how the version is
//     branch-classified when no branch is checked out, e.g. after a CI checkout
//     that leaves HEAD detached at a SHA).
//   - Detached HEAD, no override: an error, as before.
func (g *repo) resolveBranch(override string) (string, error) {
	h, err := g.r.Head()
	if err != nil {
		return "", fmt.Errorf("resolve HEAD: %w", err)
	}
	if h.Name().IsBranch() {
		actual := h.Name().Short()
		if override != "" && override != actual {
			return "", fmt.Errorf("--branch %q does not match the checked-out branch %q", override, actual)
		}
		return actual, nil
	}
	// Detached HEAD.
	if override == "" {
		return "", fmt.Errorf("HEAD is detached; givi requires a checked-out branch (or pass --branch)")
	}
	return override, nil
}

func (g *repo) headCommit() (*object.Commit, error) {
	h, err := g.r.Head()
	if err != nil {
		return nil, fmt.Errorf("resolve HEAD: %w", err)
	}
	return g.r.CommitObject(h.Hash())
}

// branchCommit resolves a local branch name to its tip commit, or (nil, nil) if
// the branch does not exist.
func (g *repo) branchCommit(name string) (*object.Commit, error) {
	ref, err := g.r.Reference(plumbing.NewBranchReferenceName(name), true)
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return nil, nil
		}
		return nil, fmt.Errorf("resolve branch %q: %w", name, err)
	}
	return g.r.CommitObject(ref.Hash())
}

// mainBranch finds the permanent release branch, preferring "main" over
// "master".
func (g *repo) mainBranch() (*object.Commit, string, error) {
	for _, name := range []string{"main", "master"} {
		c, err := g.branchCommit(name)
		if err != nil {
			return nil, "", err
		}
		if c != nil {
			return c, name, nil
		}
	}
	return nil, "", fmt.Errorf("no \"main\" or \"master\" branch found")
}

// tagCores maps a commit hash to the semver core of any release tag pointing at
// it, and (separately) a commit hash to any prerelease "reference" tag on it.
//
// A release tag is a bare release version (e.g. "v2.1.0"), which becomes a
// calculation boundary. A prerelease reference tag carries a trailing numeric
// counter (e.g. "4.5.6-foobar-x.3") and is returned in refs — it is not a
// boundary but pins an in-progress core/label/counter. Any other tag (non-semver
// after stripping "v", or a prerelease without a counter) is ignored entirely.
//
// When several USABLE tags point at the same commit and they resolve to the SAME
// version, that is fine (e.g. "1.2.3" and "v1.2.3", or a release and a matching
// duplicate). When they resolve to DIFFERENT versions the commit is ambiguous:
// its hash is recorded in conflicts with a descriptive error. That error is NOT
// raised here — a conflict only matters if the commit is actually consulted
// during calculation, so callers check conflicts lazily when they select a tag.
//
// skipped, when non-nil, is called once for every tag that is ignored (with the
// tag name and the reason), so callers can trace which tags were dropped.
func (g *repo) tagCores(skipped func(name string, err error)) (
	map[plumbing.Hash]core, map[plumbing.Hash]prereleaseRef, map[plumbing.Hash]error, error,
) {
	out := map[plumbing.Hash]core{}
	refs := map[plumbing.Hash]prereleaseRef{}
	conflicts := map[plumbing.Hash]error{}

	// Per commit, the version-id and name of the first usable tag seen, so a
	// second usable tag with a different version-id can be reported as a conflict.
	firstID := map[plumbing.Hash]string{}
	firstName := map[plumbing.Hash]string{}

	iter, err := g.r.Tags()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("list tags: %w", err)
	}
	defer iter.Close()
	err = iter.ForEach(func(ref *plumbing.Reference) error {
		name := ref.Name().Short()
		// Resolve the commit the tag points at (annotated tags dereference to
		// their target).
		target := ref.Hash()
		if to, terr := g.r.TagObject(ref.Hash()); terr == nil {
			target = to.Target
		}

		// Determine this tag's usable version, if any, as a comparable id.
		var id string
		if c, perr := parseCore(name); perr == nil {
			id = "release:" + c.String()
			// Highest release wins on same-commit duplicates (only reached for
			// equal ids after the conflict check below, so it stays the same).
			if existing, ok := out[target]; !ok || less(existing, c) {
				out[target] = c
			}
		} else if pr, perr := parsePrereleaseRef(name); perr == nil {
			id = fmt.Sprintf("ref:%s-%s.%d", pr.core, pr.label, pr.counter)
			if _, ok := refs[target]; !ok {
				refs[target] = pr
			}
		} else {
			// Neither a release nor a prerelease reference: ignore entirely.
			if skipped != nil {
				skipped(name, fmt.Errorf("not a release or prerelease reference tag"))
			}
			return nil
		}

		// Conflict bookkeeping: two usable tags with different version-ids on the
		// same commit make it ambiguous.
		if prevID, ok := firstID[target]; ok {
			if prevID != id && conflicts[target] == nil {
				conflicts[target] = fmt.Errorf(
					"conflicting version tags on %s: %q and %q", target, firstName[target], name)
			}
		} else {
			firstID[target] = id
			firstName[target] = name
		}
		return nil
	})
	if err != nil {
		return nil, nil, nil, err
	}
	return out, refs, conflicts, nil
}

func less(a, b core) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}

// firstParentChain returns the first-parent history from a commit down to the
// root, newest first.
func (g *repo) firstParentChain(from *object.Commit) ([]*object.Commit, error) {
	var chain []*object.Commit
	c := from
	for c != nil {
		chain = append(chain, c)
		if c.NumParents() == 0 {
			break
		}
		p, err := c.Parent(0)
		if err != nil {
			return nil, fmt.Errorf("walk first-parent history: %w", err)
		}
		c = p
	}
	return chain, nil
}

// mergeBase returns the best common ancestor of a and b.
func (g *repo) mergeBase(a, b *object.Commit) (*object.Commit, error) {
	bases, err := a.MergeBase(b)
	if err != nil {
		return nil, fmt.Errorf("compute merge-base: %w", err)
	}
	if len(bases) == 0 {
		return nil, fmt.Errorf("no common ancestor between commits")
	}
	return bases[0], nil
}

// parentPool returns, for every commit reachable from start (inclusive), its
// parent hashes. Parents are read directly from the commit objects. This single
// walk backs every reachability, ancestor-set, and section-counting question, so
// the history is loaded at most once.
func (g *repo) parentPool(start plumbing.Hash) (map[plumbing.Hash][]plumbing.Hash, error) {
	pool := map[plumbing.Hash][]plumbing.Hash{}
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if _, ok := pool[h]; ok {
			continue
		}
		commit, err := g.r.CommitObject(h)
		if err != nil {
			return nil, err
		}
		parents := commit.ParentHashes
		pool[h] = parents
		for _, ph := range parents {
			if _, ok := pool[ph]; !ok {
				stack = append(stack, ph)
			}
		}
	}
	return pool, nil
}

// ancestorHashesIn returns the set of hashes reachable from start (inclusive),
// following the parent edges in a pre-built pool. No git objects are loaded;
// every reachable commit must already be present in pool (guaranteed when start
// is reachable from the commit the pool was built from).
func ancestorHashesIn(start plumbing.Hash, pool map[plumbing.Hash][]plumbing.Hash) map[plumbing.Hash]bool {
	seen := map[plumbing.Hash]bool{}
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] {
			continue
		}
		seen[h] = true
		for _, ph := range pool[h] {
			if !seen[ph] {
				stack = append(stack, ph)
			}
		}
	}
	return seen
}

// mergeBranchRe extracts the merged branch from a standard git merge commit
// message, e.g. "Merge branch 'feature/cool-abc' into develop". The captured
// name is the branch ref, e.g. "feature/cool-abc".
var mergeBranchRe = regexp.MustCompile(`Merge branch '([^']+)'`)

// mergePRRe extracts the merged branch from a GitHub pull-request merge commit
// message, e.g. "Merge pull request #42 from owner/feature/cool-abc". The
// captured ref is "<owner>/<branch>", so the branch is everything after the
// first slash, e.g. "feature/cool-abc".
var mergePRRe = regexp.MustCompile(`Merge pull request #\d+ from (\S+)`)

// mergeRemoteRe extracts the merged branch from a merge of a remote-tracking
// ref, the message git writes when a fetched branch is merged directly, e.g.
// "Merge remote-tracking branch 'origin/feature/cool-abc' into develop". The
// captured ref is "<remote>/<branch>", so the branch is everything after the
// first slash, e.g. "feature/cool-abc".
var mergeRemoteRe = regexp.MustCompile(`Merge remote-tracking branch '([^']+)'`)

// mergedBranchName returns the name of the branch merged by commit c, or "" if
// c is not a recognized merge commit. The git-standard, GitHub PR, and
// remote-tracking merge message formats are all supported (short-lived branches
// are deleted, so the message is the only surviving trace of the merged branch
// name). The owner/remote prefix, when present, is stripped, e.g.
// "origin/feature/x" and "owner/feature/x" both yield "feature/x".
func mergedBranchName(c *object.Commit) string {
	if c.NumParents() < 2 {
		return ""
	}
	// The remote-tracking form must be checked before the plain "Merge branch"
	// form: its message ("Merge remote-tracking branch '<remote>/<branch>'")
	// carries a "<remote>/" prefix that has to be stripped, like the PR form.
	if m := mergeRemoteRe.FindStringSubmatch(c.Message); m != nil {
		// Drop the leading "<remote>/" (e.g. "origin/").
		if _, branch, ok := strings.Cut(m[1], "/"); ok {
			return branch
		}
	}
	if m := mergeBranchRe.FindStringSubmatch(c.Message); m != nil {
		return m[1]
	}
	if m := mergePRRe.FindStringSubmatch(c.Message); m != nil {
		// Drop the leading "<owner>/" from the head ref.
		if _, branch, ok := strings.Cut(m[1], "/"); ok {
			return branch
		}
	}
	return ""
}

// mergedBranchType returns the "type" prefix (the part before the first "/")
// of the branch merged by commit c, or "" if c is not a recognized merge
// commit or the merged branch has no prefix.
func mergedBranchType(c *object.Commit) string {
	return typePrefix(mergedBranchName(c))
}

// typePrefix returns the branch-type prefix (before the first "/"), or "" when
// the branch has no prefix.
func typePrefix(branch string) string {
	if prefix, _, ok := strings.Cut(branch, "/"); ok {
		return prefix
	}
	return ""
}

// isFeatureMerge reports whether a commit is a merge of a feature branch
// ("feature/" or its "feat/" shorthand).
func isFeatureMerge(c *object.Commit) bool {
	return isFeatureType(mergedBranchType(c))
}

// createLightweightTag creates a lightweight tag pointing at hash. It reports
// whether the tag was created (false if it already existed).
func (g *repo) createLightweightTag(name string, hash plumbing.Hash) (bool, error) {
	if _, err := g.r.Reference(plumbing.NewTagReferenceName(name), false); err == nil {
		return false, nil // already exists
	}
	if _, err := g.r.CreateTag(name, hash, nil); err != nil {
		return false, fmt.Errorf("create tag %q: %w", name, err)
	}
	return true, nil
}

// verifyTagAtHead confirms that a tag named `name` exists and points at hash,
// dereferencing annotated tags to their target commit. It errors if the tag is
// missing or points at a different commit. This guards --push-tag-to: givi only
// pushes a tag it is certain marks HEAD, whether that tag was just created by
// --tag-main or already existed (in lightweight or annotated form).
func (g *repo) verifyTagAtHead(name string, hash plumbing.Hash) error {
	ref, err := g.r.Reference(plumbing.NewTagReferenceName(name), false)
	if err != nil {
		return fmt.Errorf("tag %q does not exist", name)
	}
	// Annotated tags are tag objects that dereference to their target commit;
	// lightweight tags point at the commit directly.
	target := ref.Hash()
	if to, terr := g.r.TagObject(ref.Hash()); terr == nil {
		target = to.Target
	}
	if target != hash {
		return fmt.Errorf("tag %q points at %s, not HEAD %s; refusing to push", name, short(target), short(hash))
	}
	return nil
}

// looksLikeURL reports whether s is a remote URL rather than a bare remote
// name. It recognizes scheme URLs (https://, ssh://, git://, file://),
// scp-like syntax (git@host:owner/repo), and local paths (absolute or
// relative). A bare token such as "origin" is treated as a remote name.
func looksLikeURL(s string) bool {
	return strings.Contains(s, "://") ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, ".") ||
		(strings.Contains(s, "@") && strings.Contains(s, ":"))
}

// pushTag pushes only the single tag `name` (and nothing else) to the given
// remote, which is either the name of an already-configured remote or a remote
// URL. The refspec is restricted to the one tag ref so no branches or other
// tags are sent. An already-up-to-date remote is treated as success.
func (g *repo) pushTag(name, remote string) error {
	spec := config.RefSpec(fmt.Sprintf("refs/tags/%s:refs/tags/%s", name, name))

	// Prefer an existing configured remote so its stored URL and options apply.
	if _, err := g.r.Remote(remote); err == nil {
		return normalizePushErr(g.r.Push(&git.PushOptions{
			RemoteName: remote,
			RefSpecs:   []config.RefSpec{spec},
		}), name, remote)
	}

	// Not a configured remote. A bare name that does not exist is an error; a
	// URL is pushed to directly via an anonymous remote.
	if !looksLikeURL(remote) {
		return fmt.Errorf("remote %q not found; pass an existing remote name or a URL", remote)
	}
	rc := &config.RemoteConfig{Name: "givi-push-tag-to", URLs: []string{remote}}
	if err := rc.Validate(); err != nil {
		return fmt.Errorf("invalid remote URL %q: %w", remote, err)
	}
	r := git.NewRemote(g.r.Storer, rc)
	return normalizePushErr(r.Push(&git.PushOptions{
		RemoteName: rc.Name,
		RemoteURL:  remote,
		RefSpecs:   []config.RefSpec{spec},
	}), name, remote)
}

// normalizePushErr turns go-git's "already up-to-date" sentinel into success
// (the tag is present on the remote, which is all --push-tag-to promises) and
// wraps any real failure with context.
func normalizePushErr(err error, name, remote string) error {
	if err == nil || err == git.NoErrAlreadyUpToDate {
		return nil
	}
	return fmt.Errorf("push tag %q to %q: %w", name, redactURL(remote), err)
}

// redactURL strips any userinfo (e.g. a token embedded as
// https://x-access-token:TOKEN@host/...) from a remote URL so it is safe to
// print in logs and errors. Non-URL remotes (names, scp-like, local paths) are
// returned unchanged, since they carry no inline credentials.
func redactURL(remote string) string {
	if !strings.Contains(remote, "://") {
		return remote
	}
	u, err := url.Parse(remote)
	if err != nil || u.User == nil {
		return remote
	}
	// "redacted" is used verbatim (url.String would percent-encode characters
	// like '*'); letters pass through unchanged.
	u.User = url.User("redacted")
	return u.String()
}
