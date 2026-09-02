package main

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
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

// repo wraps a go-git repository with the helpers genver needs. All operations
// are local; nothing here touches the network.
type repo struct {
	r     *git.Repository
	out   fileWriter          // persists --write-to output
	store *filesystem.Storage // non-nil when descriptors are kept open (needs Close)

	// preferredRemote is the remote whose tracking refs are consulted first when
	// a reference branch (main/master/develop) has no local head. It is the
	// upstream remote configured for the branch genver is computing a version
	// for (branch.<branch>.remote), so multi-remote clones resolve reference
	// branches against the same remote as the branch under calculation. Empty
	// when the branch has no configured upstream, in which case resolution falls
	// back to "origin" and then a unique match across remotes.
	preferredRemote string

	// trace, when non-nil, receives timestamped debug lines describing how
	// reference branches are resolved (local vs remote-tracking ref, and why).
	// It shares the calculator's --debug sink so all trace output is unified.
	trace io.Writer
}

// logf writes a timestamped trace line when tracing is enabled (a no-op
// otherwise), using the same format as the calculator's trace.
func (g *repo) logf(format string, args ...any) {
	tracef(g.trace, format, args...)
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
			ExclusiveAccess: true, // genver never mutates the repo while reading
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
		return "", fmt.Errorf("HEAD is detached; genver requires a checked-out branch")
	}
	return h.Name().Short(), nil
}

// resolveBranch determines the branch name genver should compute a version for,
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
//
// A non-empty override is first validated against git's branch-name rules
// (git-check-ref-format); an invalid name is rejected before any of the above.
func (g *repo) resolveBranch(override string) (string, error) {
	// A non-empty override must be a valid git branch name; fail early on
	// characters git forbids (spaces, ~ ^ : ?, control chars, "..", etc.) rather
	// than carrying a malformed name into classification and version output. An
	// empty override means "no override" and is handled below.
	if override != "" {
		if err := plumbing.NewBranchReferenceName(override).Validate(); err != nil {
			return "", fmt.Errorf("invalid --branch name %q: %w", override, err)
		}
	}
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
		return "", fmt.Errorf("HEAD is detached; genver requires a checked-out branch (or pass --branch)")
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

// branchUpstreamRemote returns the remote configured as branch's upstream
// (branch.<branch>.remote in git config), or "" when the branch has no
// configured upstream. This is the remote genver prefers when resolving
// reference branches that exist only as remote-tracking refs.
func (g *repo) branchUpstreamRemote(branch string) string {
	cfg, err := g.r.Config()
	if err != nil {
		return ""
	}
	b, ok := cfg.Branches[branch]
	if !ok || b == nil {
		return ""
	}
	return b.Remote
}

// setPreferredRemoteFor records, as the preferred remote for later reference-
// branch resolution, the upstream remote of the branch genver is computing a
// version for. A no-op (leaves preferredRemote empty) when the branch has no
// configured upstream.
func (g *repo) setPreferredRemoteFor(branch string) {
	g.preferredRemote = g.branchUpstreamRemote(branch)
}

// branchCommit resolves a reference branch name to its tip commit, or (nil, nil)
// if the branch cannot be found.
//
// Resolution reconciles the local head (refs/heads/<name>) with the
// remote-tracking ref (refs/remotes/<remote>/<name>):
//
//   - Only a local head exists: use it.
//   - Only a remote-tracking ref exists (the fresh-clone / CI layout, where just
//     the checked-out branch has a local head): use the remote. The upstream
//     remote of the branch under calculation is preferred, then "origin", then a
//     unique match across remotes (see remoteBranchCommit).
//   - Both exist: the REMOTE wins, but only when the local head is not ahead of
//     it (every local commit is reachable from the remote tip) AND the two share
//     a common merge base. This adopts the more up-to-date remote when local is
//     merely behind-or-equal. If the local head has commits the remote lacks
//     (local ahead or diverged), or the histories are unrelated (no merge base),
//     the LOCAL head wins, so local work is never silently discarded.
func (g *repo) branchCommit(name string) (*object.Commit, error) {
	localRef, err := g.r.Reference(plumbing.NewBranchReferenceName(name), true)
	switch {
	case err == nil:
		// Local head exists; fall through to reconcile with any remote below.
	case err == plumbing.ErrReferenceNotFound:
		// No local head: use the remote-tracking fallback.
		return g.remoteBranchCommit(name)
	default:
		return nil, fmt.Errorf("resolve branch %q: %w", name, err)
	}

	local, err := g.r.CommitObject(localRef.Hash())
	if err != nil {
		return nil, err
	}

	remote, err := g.remoteBranchCommit(name)
	if err != nil {
		return nil, err
	}
	if remote == nil {
		// No remote-tracking ref: local is the only source.
		g.logf("resolve branch %q: local %s (no remote-tracking ref)", name, short(local.Hash))
		return local, nil
	}

	prefer, reason, err := g.preferRemoteOverLocal(local, remote)
	if err != nil {
		return nil, err
	}
	if prefer {
		g.logf("resolve branch %q: remote %s wins over local %s (%s)",
			name, short(remote.Hash), short(local.Hash), reason)
		return remote, nil
	}
	g.logf("resolve branch %q: local %s wins over remote %s (%s)",
		name, short(local.Hash), short(remote.Hash), reason)
	return local, nil
}

// preferRemoteOverLocal reports whether the remote tip should be used in place
// of the local tip for a reference branch present in both, along with a short
// human-readable reason for the decision (for --debug tracing). The remote wins
// only when local is not ahead of remote (local is an ancestor of, or equal to,
// remote) and the two share a common merge base; otherwise local wins.
func (g *repo) preferRemoteOverLocal(local, remote *object.Commit) (bool, string, error) {
	if local.Hash == remote.Hash {
		return true, "local and remote point at the same commit", nil
	}
	// A common merge base is required (related histories). Unrelated histories
	// -> keep local.
	bases, err := local.MergeBase(remote)
	if err != nil {
		return false, "", fmt.Errorf("compute merge-base of local and remote: %w", err)
	}
	if len(bases) == 0 {
		return false, "unrelated histories: no common merge base", nil
	}
	// Local must have no commits absent from remote: local is an ancestor of
	// remote. (Equality is handled above.)
	localBehind, err := local.IsAncestor(remote)
	if err != nil {
		return false, "", fmt.Errorf("compare local and remote ancestry: %w", err)
	}
	if localBehind {
		return true, "local is behind remote (all local commits are in remote) with a common base", nil
	}
	return false, "local has commits not in remote (local is ahead or diverged)", nil
}

// remoteBranchCommit resolves a branch name against remote-tracking refs
// (refs/remotes/<remote>/<name>), returning (nil, nil) when no remote carries
// the branch. The upstream remote of the branch under calculation
// (preferredRemote) wins first, then "origin"; otherwise the branch must be
// carried by exactly one remote, else the name is ambiguous.
func (g *repo) remoteBranchCommit(name string) (*object.Commit, error) {
	// Try the explicitly preferred remotes in order: the branch-under-
	// calculation's configured upstream first (when set), then "origin". The
	// first that carries the branch wins outright — no ambiguity check, since
	// the choice is deliberate.
	for _, remote := range []string{g.preferredRemote, "origin"} {
		if remote == "" {
			continue
		}
		c, err := g.r.Reference(plumbing.NewRemoteReferenceName(remote, name), true)
		if err == nil {
			kind := "origin"
			if remote == g.preferredRemote {
				kind = "preferred upstream"
			}
			commit, cerr := g.r.CommitObject(c.Hash())
			if cerr != nil {
				return nil, cerr
			}
			g.logf("remote resolve %q: matched %s/%s (%s) at %s", name, remote, name, kind, short(commit.Hash))
			return commit, nil
		}
		if err != plumbing.ErrReferenceNotFound {
			return nil, fmt.Errorf("resolve remote branch %q: %w", remote+"/"+name, err)
		}
	}

	// No preferred/origin match: scan every remote-tracking ref for a unique
	// match. A remote-tracking ref has the form refs/remotes/<remote>/<branch>,
	// where <branch> may itself contain slashes (e.g. "feature/x"). To split off
	// the remote segment reliably, match against the configured remote names
	// rather than guessing on slashes.
	remoteNames, err := g.remoteNames()
	if err != nil {
		return nil, err
	}
	refs, err := g.r.References()
	if err != nil {
		return nil, fmt.Errorf("list references: %w", err)
	}
	defer refs.Close()

	found := map[string]plumbing.Hash{} // remote name -> tip hash
	if err := refs.ForEach(func(ref *plumbing.Reference) error {
		if !ref.Name().IsRemote() || ref.Type() == plumbing.SymbolicReference {
			// Skip non-remote refs and symbolic refs like
			// refs/remotes/origin/HEAD.
			return nil
		}
		for _, remote := range remoteNames {
			if ref.Name() == plumbing.NewRemoteReferenceName(remote, name) {
				found[remote] = ref.Hash()
			}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("scan remote branches for %q: %w", name, err)
	}

	switch len(found) {
	case 0:
		g.logf("remote resolve %q: no local head and no remote-tracking ref found", name)
		return nil, nil
	case 1:
		for remote, h := range found {
			commit, err := g.r.CommitObject(h)
			if err != nil {
				return nil, err
			}
			g.logf("remote resolve %q: matched unique remote %s/%s at %s", name, remote, name, short(commit.Hash))
			return commit, nil
		}
	}
	labels := make([]string, 0, len(found))
	for r := range found {
		labels = append(labels, r+"/"+name)
	}
	sort.Strings(labels)
	return nil, fmt.Errorf(
		"branch %q is ambiguous: found on multiple remotes (%s); no local branch or origin/%s to disambiguate",
		name, strings.Join(labels, ", "), name)
}

// remoteNames returns the configured remote names, used to split a
// remote-tracking ref (refs/remotes/<remote>/<branch>) into its remote and
// branch segments when the branch name itself may contain slashes.
func (g *repo) remoteNames() ([]string, error) {
	remotes, err := g.r.Remotes()
	if err != nil {
		return nil, fmt.Errorf("list remotes: %w", err)
	}
	names := make([]string, 0, len(remotes))
	for _, rem := range remotes {
		names = append(names, rem.Config().Name)
	}
	return names, nil
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
// it, whether that release tag is "v"-prefixed, and (separately) a commit hash
// to any prerelease "reference" tag on it.
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
	map[plumbing.Hash]core, map[plumbing.Hash]bool, map[plumbing.Hash]prereleaseRef, map[plumbing.Hash]error, error,
) {
	out := map[plumbing.Hash]core{}
	// vPrefix records, per commit, whether its selected release tag is spelled
	// with a leading "v" (e.g. "v1.2.3"). When a commit carries both a
	// "v"-prefixed and a bare release tag for the same version, the "v" form
	// wins (true). It is consulted to default the output/tag format to the
	// boundary tag's own spelling.
	vPrefix := map[plumbing.Hash]bool{}
	refs := map[plumbing.Hash]prereleaseRef{}
	conflicts := map[plumbing.Hash]error{}

	// Per commit, the version-id and name of the first usable tag seen, so a
	// second usable tag with a different version-id can be reported as a conflict.
	firstID := map[plumbing.Hash]string{}
	firstName := map[plumbing.Hash]string{}

	iter, err := g.r.Tags()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("list tags: %w", err)
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
			hasV := strings.HasPrefix(strings.TrimSpace(name), "v")
			// Highest release wins on same-commit duplicates (only reached for
			// equal ids after the conflict check below, so it stays the same).
			if existing, ok := out[target]; !ok || less(existing, c) {
				out[target] = c
				// A strictly higher release resets the spelling decision to
				// this tag's own.
				vPrefix[target] = hasV
			} else if existing == c && hasV {
				// Same version as the winner and "v"-prefixed: "v" wins over bare.
				vPrefix[target] = true
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
		return nil, nil, nil, nil, err
	}
	return out, vPrefix, refs, conflicts, nil
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

// mergeGitHubPRRe extracts the merged branch from a GitHub pull-request merge
// commit message, e.g. "Merge pull request #42 from owner/feature/cool-abc".
// The captured ref is "<owner>/<branch>", so the branch is everything after the
// first slash, e.g. "feature/cool-abc".
var mergeGitHubPRRe = regexp.MustCompile(`Merge pull request #\d+ from (\S+)`)

// mergeRemoteRe extracts the merged branch from a merge of a remote-tracking
// ref, the message git writes when a fetched branch is merged directly, e.g.
// "Merge remote-tracking branch 'origin/feature/cool-abc' into develop". The
// captured ref is "<remote>/<branch>", so the branch is everything after the
// first slash, e.g. "feature/cool-abc".
var mergeRemoteRe = regexp.MustCompile(`Merge remote-tracking branch '([^']+)'`)

// mergeBitbucketServerPRRe extracts the merged branch from a Bitbucket Server
// (formerly Stash / Data Center) pull-request merge commit message. This is
// specific to Bitbucket Server: Bitbucket Cloud writes a different message and
// is NOT matched here. Bitbucket Server writes a two-line message where the
// subject is "Pull request #<n>: <title>" and the body carries the actual refs,
// e.g.
//
//	Pull request #123: ABC-1234 my cool feature
//
//	Merge in PROJECT/repo from feature/cool-abc to develop
//
// The whole shape is matched (subject line included) so a stray "Merge in ..."
// body without the Bitbucket Server "Pull request #<n>:" subject is not
// mistaken for this format. The captured group is the SOURCE branch verbatim
// (already unprefixed, e.g. "feature/cool-abc"). Branch names cannot contain
// whitespace, so the source, target, and project/repo tokens are each matched
// as runs of non-space characters. "(?s)" lets "." span the blank line between
// subject and body, and "\A" anchors the subject at the start of the message.
var mergeBitbucketServerPRRe = regexp.MustCompile(`(?s)\APull request #\d+:.*?\bMerge in \S+ from (\S+) to \S+`)

// mergedBranchName returns the name of the branch merged by commit c, or "" if
// c is not a recognized merge commit. The git-standard, GitHub PR, Bitbucket
// Server PR, and remote-tracking merge message formats are all supported
// (short-lived branches are deleted, so the message is the only surviving trace
// of the merged branch name). The owner/remote prefix, when present, is
// stripped, e.g. "origin/feature/x" and "owner/feature/x" both yield
// "feature/x".
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
	if m := mergeGitHubPRRe.FindStringSubmatch(c.Message); m != nil {
		// Drop the leading "<owner>/" from the head ref.
		if _, branch, ok := strings.Cut(m[1], "/"); ok {
			return branch
		}
	}
	// Bitbucket Server's "Merge in <project>/<repo> from <source> to <target>"
	// body carries the source branch verbatim (no owner/remote prefix to strip).
	if m := mergeBitbucketServerPRRe.FindStringSubmatch(c.Message); m != nil {
		return m[1]
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
// missing or points at a different commit. This guards --push-tag-to: genver only
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
	rc := &config.RemoteConfig{Name: "genver-push-tag-to", URLs: []string{remote}}
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
