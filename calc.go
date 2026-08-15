package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
	cgobject "github.com/go-git/go-git/v5/plumbing/object/commitgraph"
)

// result is a computed version, split so the --format flag can render
// individual components (base, prerelease tail, major/minor/patch, branch).
type result struct {
	core       core
	prerelease string // "" on the main branch
	isMain     bool
	branch     string        // the branch the version was computed for
	headHash   plumbing.Hash // HEAD commit the version was computed for
}

// version renders the full version string.
func (r result) version() (string, error) {
	return format(r.core, r.prerelease)
}

// boundary is a develop-line commit that starts a new "section": either the
// develop tip that a tagged main merge released, or the repository root.
type boundary struct {
	commit *object.Commit
	core   core
}

// calculator holds the state needed to compute a version.
type calculator struct {
	g            *repo
	idx          cgobject.CommitNodeIndex // commit-graph-backed parent lookups
	boundaries   []boundary
	boundaryCore map[plumbing.Hash]core // boundary commit hash -> release core
	tagCore      map[plumbing.Hash]core // commit hash -> core of a tag on it
	trace        io.Writer              // nil disables tracing
}

func newCalculator(g *repo) (*calculator, error) {
	return newCalculatorTrace(g, nil)
}

// newCalculatorTrace builds a calculator that logs every calculation step to
// trace (unless trace is nil), using the commit-graph when available.
func newCalculatorTrace(g *repo, trace io.Writer) (*calculator, error) {
	return newCalculatorOpts(g, trace, true)
}

// newCalculatorOpts builds a calculator. When useCommitGraph is false the
// commit-graph is ignored even if present, forcing the object-store path.
func newCalculatorOpts(g *repo, trace io.Writer, useCommitGraph bool) (*calculator, error) {
	idx, usingGraph := g.commitNodeIndex(useCommitGraph)
	c := &calculator{g: g, idx: idx, trace: trace}
	switch {
	case usingGraph:
		c.logf("using commit-graph for parent lookups (fast path)")
	case !useCommitGraph:
		c.logf("commit-graph disabled by --no-commit-graph; decoding commit objects for parent lookups (slow path)")
	default:
		c.logf("no commit-graph found; decoding commit objects for parent lookups (slow path)")
	}

	// Cache the tag->core map once; it is consulted repeatedly.
	tc, err := g.tagCores()
	if err != nil {
		return nil, err
	}
	c.tagCore = tc

	// developBoundaries populates c.boundaries and c.boundaryCore.
	if _, err := c.developBoundaries(); err != nil {
		return nil, err
	}

	c.logf("discovered %d release boundary/boundaries:", len(c.boundaries))
	for _, b := range c.boundaries {
		c.logf("  boundary %s -> %s", short(b.commit.Hash), b.core)
	}
	return c, nil
}

// logf writes a trace line if tracing is enabled. It is a no-op otherwise.
// Each line is prefixed with a wall-clock timestamp at millisecond precision.
func (c *calculator) logf(format string, args ...any) {
	if c.trace == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000000000")
	fmt.Fprintf(c.trace, ts+": "+format+"\n", args...)
}

// short renders an abbreviated commit hash for readable trace output.
func short(h plumbing.Hash) string {
	s := h.String()
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// developBoundaries builds the set of release boundaries on the develop line.
//
// A release boundary marks a point whose release core later releases build on.
// Boundaries come from two sources:
//
//   - Every semver tag is a boundary at its (second-parent) released tip. This
//     is cheap — it reads only the tagged commits — and on its own fully
//     describes a repository that tags all of its releases.
//   - Untagged release points ABOVE the newest tag on main's first-parent
//     chain: a forward pass over just that region tracks the release core
//     exactly as mainVersion does and registers a boundary at each release
//     point — a merge's second parent (the released tip), so an untagged release
//     behaves like a tagged one, and each direct commit on main (which advances
//     the release core in place), so a develop branched from or merging that
//     main commit builds on the main core rather than the 0.1.0 root.
//
// Restricting the forward pass to the region above the newest tag is the key to
// performance: everything at or below that tag is already described by the tags
// themselves, so we never walk history for it. The expensive per-untagged-merge
// core computation (developVersion / directMergeBump, each a history walk) runs
// only for the typically small set of commits made since the last release —
// avoiding a quadratic blowup on large, densely tagged repositories, especially
// on the --no-commit-graph path. When there is no tag at all, the pass covers
// the whole chain starting from the 0.1.0 root, which is inherently the cost of
// an untagged repository.
//
// The repository root is always a boundary (core 0.1.0, or a tag on it).
// Boundaries are populated into c.boundaries / c.boundaryCore as the pass
// proceeds so that computing an untagged develop-release merge's core (via
// developVersion) can rely on the earlier boundaries already registered.
func (c *calculator) developBoundaries() ([]boundary, error) {
	c.boundaryCore = map[plumbing.Hash]core{}
	c.boundaries = nil
	seen := map[plumbing.Hash]bool{}
	add := func(bc *object.Commit, cr core) {
		if seen[bc.Hash] {
			return
		}
		seen[bc.Hash] = true
		c.boundaries = append(c.boundaries, boundary{commit: bc, core: cr})
		c.boundaryCore[bc.Hash] = cr
	}

	// Cheap source: every semver tag is a boundary at its released tip. This
	// alone describes a repository that tags its releases, with no history walk.
	for h, cr := range c.tagCore {
		commit, err := c.g.r.CommitObject(h)
		if err != nil {
			continue // tag points at a non-commit object; ignore
		}
		bc := commit
		if commit.NumParents() >= 2 {
			// Release merge: the released tip is the second parent.
			p, perr := commit.Parent(1)
			if perr != nil {
				return nil, perr
			}
			bc = p
		}
		add(bc, cr)
	}

	mainCommit, _, err := c.g.mainBranch()
	if err != nil {
		return nil, err
	}
	chain, err := c.g.firstParentChain(mainCommit) // newest first
	if err != nil {
		return nil, err
	}

	// Always include the root at 0.1.0 (a no-op if it was already tagged).
	root := chain[len(chain)-1]
	add(root, core{0, 1, 0})

	// Find the newest tagged commit on main's first-parent chain. Everything at
	// or below it is already described by the tags added above; only the region
	// above it needs the forward core computation for untagged releases.
	baseIdx := len(chain) - 1
	baseCore := core{0, 1, 0}
	for i, cm := range chain {
		if tc, ok := c.tagCore[cm.Hash]; ok {
			baseIdx = i
			baseCore = tc
			break
		}
	}

	// Forward pass over the untagged region above the newest tag, oldest first.
	cur := baseCore
	for i := baseIdx - 1; i >= 0; i-- {
		cm := chain[i]
		if tc, tagged := c.tagCore[cm.Hash]; tagged {
			// No on-chain tags exist above the newest one, but stay robust.
			cur = tc
			continue
		}
		if cm.NumParents() < 2 {
			// Direct commit on main: advance the running core (a patch bump
			// unless overridden). The commit itself is a release boundary at that
			// core, so a develop branched from (or that merges) this point on main
			// builds on the main core here rather than falling back to the root.
			cur = cur.bump(maxBump(bumpPatch, bumpFromMessage(cm.Message)))
			add(cm, cur)
			continue
		}
		// Merge commit: its second parent is the released tip.
		p, perr := cm.Parent(1)
		if perr != nil {
			return nil, perr
		}
		if c.isDevelopReleaseMerge(cm) {
			// Release merge from develop: the core is develop's core at the
			// merged tip, built on the boundaries registered so far.
			dc, _, derr := c.developVersion(p)
			if derr != nil {
				return nil, derr
			}
			cur = dc
		} else {
			// Direct merge of a non-develop branch: advance the core once.
			bump, berr := c.directMergeBump(cm)
			if berr != nil {
				return nil, berr
			}
			cur = cur.bump(bump)
		}
		add(p, cur)
	}

	return c.boundaries, nil
}

// isBoundary reports the boundary core if x is itself a release boundary.
func (c *calculator) isBoundary(x *object.Commit) (core, bool) {
	cr, ok := c.boundaryCore[x.Hash]
	return cr, ok
}

// sectionScan holds the result of analysing a single develop "section": the
// commits reachable from the section's start but not from the release boundary
// it builds on (equivalent to `git rev-list base..start`).
type sectionScan struct {
	baseCore core          // release core of the boundary the section builds on
	baseHash plumbing.Hash // that boundary's commit hash
	count    int           // number of commits in the section (base..start)
	bump     bumpKind      // strongest version bump those commits imply
}

// scanSection determines the release boundary a commit builds on and analyses
// the commits since that boundary. The base is the reachable boundary with the
// highest release core (the latest release in start's ancestry); the section is
// then everything reachable from start but NOT from the base, matching
// `git rev-list base..start`. Excluding the base's full ancestor set (rather
// than just stopping at the boundary commit) is required for correct counts in
// repositories with cross-merges, where ancestors of the base can otherwise be
// reached via paths that bypass the boundary commit itself.
//
// When excludeStart is true, a boundary at start itself is not a base candidate
// (start is treated as an ordinary commit), yielding the previous section for a
// start that is itself a boundary.
func (c *calculator) scanSection(start *object.Commit, excludeStart bool) (sectionScan, error) {
	// Walk the parent edges reachable from start exactly once (cheap: no commit
	// objects are decoded when a commit-graph is present). Which boundary is the
	// base and how many commits sit above it are then answered from this pool in
	// memory.
	pool, err := parentPool(c.idx, start.Hash)
	if err != nil {
		return sectionScan{}, err
	}

	res, err := c.scanSectionInPool(start.Hash, excludeStart, pool)
	if err != nil {
		return sectionScan{}, err
	}
	if res.baseHash.IsZero() {
		return sectionScan{}, fmt.Errorf("no release boundary found for commit %s", start.Hash)
	}
	return res, nil
}

// scanSectionInPool performs the section analysis against a pre-built parent
// pool. The base is the reachable boundary with the highest release core (the
// latest release in start's ancestry); the section is everything reachable from
// start but NOT from the base, matching `git rev-list base..start`. When no
// boundary is reachable the returned baseHash is the zero hash.
func (c *calculator) scanSectionInPool(start plumbing.Hash, excludeStart bool, pool map[plumbing.Hash][]plumbing.Hash) (sectionScan, error) {
	var res sectionScan
	found := false
	for i := range c.boundaries {
		b := &c.boundaries[i]
		if _, reachable := pool[b.commit.Hash]; !reachable {
			continue
		}
		if excludeStart && b.commit.Hash == start {
			continue
		}
		if !found || less(res.baseCore, b.core) {
			res.baseCore = b.core
			res.baseHash = b.commit.Hash
			found = true
		}
	}
	if !found {
		return sectionScan{}, nil
	}

	// Count and bump over base..start: reachable from start, excluding the base
	// and everything reachable from it.
	exclude := ancestorHashesIn(res.baseHash, pool)
	count, bump, err := c.countAndBumpInPool(start, exclude, pool)
	if err != nil {
		return sectionScan{}, err
	}
	res.count, res.bump = count, bump
	return res, nil
}

// countAndBumpInPool walks the commits reachable from start but not in exclude,
// following the parent edges in a pre-built pool, returning their number and the
// strongest version bump they imply. Only the commits actually in the section
// have their full object decoded (to read the message and detect feature
// merges) — the reachability edges come from the cheap pool.
func (c *calculator) countAndBumpInPool(start plumbing.Hash, exclude map[plumbing.Hash]bool, pool map[plumbing.Hash][]plumbing.Hash) (int, bumpKind, error) {
	seen := map[plumbing.Hash]bool{}
	stack := []plumbing.Hash{start}
	count := 0
	bump := bumpNone
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] || exclude[h] {
			continue
		}
		seen[h] = true
		parents, ok := pool[h]
		if !ok {
			continue
		}
		count++
		// The strongest possible bump is major; once reached, no need to decode
		// further commits for their bump. The message and parent count are only
		// needed to raise the bump, so decode the full commit lazily.
		if bump < bumpMajor {
			commit, err := c.g.r.CommitObject(h)
			if err != nil {
				return 0, bumpNone, err
			}
			b := maxBump(bumpPatch, bumpFromMessage(commit.Message))
			if isFeatureMerge(commit) {
				b = maxBump(b, bumpMinor)
			}
			bump = maxBump(bump, b)
		}
		for _, ph := range parents {
			if !seen[ph] && !exclude[ph] {
				stack = append(stack, ph)
			}
		}
	}
	return count, bump, nil
}

// developVersion computes the (core, counter) for a develop commit.
func (c *calculator) developVersion(head *object.Commit) (core, int, error) {
	// If HEAD is itself a release boundary, the current section is empty: keep
	// the released core and report the previous section's commit count.
	if bcore, ok := c.isBoundary(head); ok {
		c.logf("develop: HEAD %s is itself a release boundary (%s); section is empty", short(head.Hash), bcore)
		scan, err := c.scanSection(head, true)
		if err != nil {
			// No lower boundary (HEAD is the root boundary): the section that
			// ends here is empty, so the counter is 0.
			c.logf("develop: no lower boundary; counter = 0")
			return bcore, 0, nil
		}
		c.logf("develop: previous boundary %s; counter = %d; core = %s", short(scan.baseHash), scan.count, bcore)
		return bcore, scan.count, nil
	}

	scan, err := c.scanSection(head, false)
	if err != nil {
		return core{}, 0, err
	}
	out := scan.baseCore.bump(scan.bump)
	c.logf("develop: section starts at boundary %s (%s); bump = %s; %d commit(s); core %s -> %s",
		short(scan.baseHash), scan.baseCore, scan.bump, scan.count, scan.baseCore, out)
	return out, scan.count, nil
}

// mainVersion computes the release core for a commit on the main branch.
func (c *calculator) mainVersion(head *object.Commit) (core, error) {
	chain, err := c.g.firstParentChain(head)
	if err != nil {
		return core{}, err
	}

	// Find the nearest tagged commit on the first-parent chain.
	baseIdx := len(chain) - 1
	baseCore := core{0, 1, 0} // root default
	for i, cm := range chain {
		if bcore, ok := c.tagCore[cm.Hash]; ok {
			baseCore = bcore
			baseIdx = i
			break
		}
	}

	if baseIdx < len(chain)-1 || len(c.boundaries) > 0 {
		c.logf("main: base %s at %s (%d commit(s) above it on first-parent chain)", baseCore, short(chain[baseIdx].Hash), baseIdx)
	} else {
		c.logf("main: no tags; base = root %s (%s)", baseCore, short(chain[baseIdx].Hash))
	}

	cur := baseCore
	// Apply commits above the base, oldest first.
	for i := baseIdx - 1; i >= 0; i-- {
		cm := chain[i]
		switch {
		case cm.NumParents() < 2:
			// Direct commit on main: patch bump unless overridden.
			bump := maxBump(bumpPatch, bumpFromMessage(cm.Message))
			next := cur.bump(bump)
			c.logf("main: direct commit %s -> %s bump: %s -> %s", short(cm.Hash), bump, cur, next)
			cur = next

		case c.isDevelopReleaseMerge(cm):
			// Release merge from develop: the release core is develop's core at
			// the merged tip (second parent).
			p, err := cm.Parent(1)
			if err != nil {
				return core{}, err
			}
			dc, _, err := c.developVersion(p)
			if err != nil {
				return core{}, err
			}
			c.logf("main: release merge %s -> core from develop tip = %s", short(cm.Hash), dc)
			cur = dc

		default:
			// Direct merge of a non-develop branch into main (a hotfix-style
			// flow). A feature-branch merge bumps minor; any other branch bumps
			// patch. Either floor is raised when a merged-in commit requests a
			// stronger bump. The whole merge advances the core exactly once.
			branch := mergedBranchName(cm)
			bump, err := c.directMergeBump(cm)
			if err != nil {
				return core{}, err
			}
			next := cur.bump(bump)
			c.logf("main: direct merge of %q %s -> %s bump: %s -> %s", branch, short(cm.Hash), bump, cur, next)
			cur = next
		}
	}
	return cur, nil
}

// isDevelopReleaseMerge reports whether a merge commit on main is a release
// merge from the develop integration branch (as opposed to a direct merge of a
// feature/other branch into main). An unrecognized merge message defaults to a
// release merge, preserving the historical "every main merge releases develop"
// behavior for repositories whose merge commits givi cannot attribute.
func (c *calculator) isDevelopReleaseMerge(cm *object.Commit) bool {
	name := mergedBranchName(cm)
	return name == "" || name == "develop"
}

// directMergeBump computes how far a direct merge of a non-develop branch into
// main advances the release core. The merge advances the core exactly once: a
// feature-branch merge has a minor floor, any other branch a patch floor. That
// floor is then raised if any commit the merge brought in requests a stronger
// bump (via a "+semver:" directive or by itself being a feature merge). The
// commits considered are those reachable from the merged tip (second parent)
// but not from main's prior tip (first parent), i.e. exactly what the merge
// introduced.
func (c *calculator) directMergeBump(cm *object.Commit) (bumpKind, error) {
	floor := bumpPatch
	if isFeatureMerge(cm) {
		floor = bumpMinor
	}
	p0, err := cm.Parent(0)
	if err != nil {
		return bumpNone, err
	}
	p1, err := cm.Parent(1)
	if err != nil {
		return bumpNone, err
	}
	pool, err := parentPool(c.idx, cm.Hash)
	if err != nil {
		return bumpNone, err
	}
	exclude := ancestorHashesIn(p0.Hash, pool)
	_, rangeBump, err := c.countAndBumpInPool(p1.Hash, exclude, pool)
	if err != nil {
		return bumpNone, err
	}
	return maxBump(floor, rangeBump), nil
}

// integrationBranch returns the tip and name of the branch that short-lived
// branches integrate into: the permanent "develop" branch when it exists,
// otherwise the main branch (the flow where short-lived branches are created
// directly off main). This is what a non-main, non-develop branch is versioned
// relative to.
func (c *calculator) integrationBranch() (*object.Commit, string, error) {
	developTip, err := c.g.branchCommit("develop")
	if err != nil {
		return nil, "", err
	}
	if developTip != nil {
		return developTip, "develop", nil
	}
	mainCommit, name, err := c.g.mainBranch()
	if err != nil {
		return nil, "", err
	}
	return mainCommit, name, nil
}

// otherVersion computes the (core, counter) for a non-main, non-develop branch.
//
// The branch is versioned relative to the integration branch it was cut from:
// "develop" when that permanent branch exists, otherwise "main" (the flow where
// short-lived branches are created directly off main). In the main-based flow
// every main commit above the latest tag is itself a release boundary, so the
// merge-base with main is a boundary and the "section" below the branch carries
// no accumulated bump — the branch builds straight on main's release core.
func (c *calculator) otherVersion(head *object.Commit, branch string) (core, int, error) {
	integrationTip, integration, err := c.integrationBranch()
	if err != nil {
		return core{}, 0, err
	}
	mb, err := c.g.mergeBase(head, integrationTip)
	if err != nil {
		return core{}, 0, err
	}
	c.logf("other: merge-base with %s is %s", integration, short(mb.Hash))

	// The merge-base is an ancestor of head, so one parent pool walked from head
	// holds everything both the develop-section scan and the branch count need.
	pool, err := parentPool(c.idx, head.Hash)
	if err != nil {
		return core{}, 0, err
	}

	// Determine the release core the integration section builds on, and the bump
	// that section has accumulated so far (at the merge-base). A single
	// section-scan handles both the boundary and non-boundary merge-base cases.
	var belowCore core
	sectionBump := bumpNone
	if bcore, ok := c.isBoundary(mb); ok {
		belowCore = bcore
		c.logf("other: merge-base is a release boundary (%s); %s section bump = none", bcore, integration)
	} else {
		scan, err := c.scanSectionInPool(mb.Hash, false, pool)
		if err != nil {
			return core{}, 0, err
		}
		if scan.baseHash.IsZero() {
			return core{}, 0, fmt.Errorf("no release boundary found for commit %s", mb.Hash)
		}
		belowCore = scan.baseCore
		sectionBump = scan.bump
		c.logf("other: %s section starts at %s (%s); %s section bump = %s", integration, short(scan.baseHash), scan.baseCore, integration, sectionBump)
	}

	// The branch's own commits (reachable from head but not from the merge-base)
	// contribute at least a patch bump. Exclude everything reachable from the
	// merge-base so the count matches `git rev-list mb..head`.
	mbSet := ancestorHashesIn(mb.Hash, pool)
	n, branchBump, err := c.countAndBumpInPool(head.Hash, mbSet, pool)
	if err != nil {
		return core{}, 0, err
	}
	c.logf("other: branch's own commits bump = %s", branchBump)
	eff := maxBump(sectionBump, branchBump)
	if isFeatureBranch(branch) {
		eff = maxBump(eff, bumpMinor) // feature increment takes precedence
		c.logf("other: feature branch -> effective bump forced to at least minor")
	}

	out := belowCore.bump(eff)
	c.logf("other: effective bump = %s; %d commit(s) since merge-base; core %s -> %s", eff, n, belowCore, out)
	return out, n, nil
}

// isFeatureBranch reports whether the branch's type prefix (before the first
// "/") marks it as a feature branch ("feature" or its "feat" shorthand).
func isFeatureBranch(branch string) bool {
	i := strings.Index(branch, "/")
	return i >= 0 && isFeatureType(branch[:i])
}

func isFeatureType(prefix string) bool {
	return prefix == "feature" || prefix == "feat"
}

// Calculate produces the version result for the current branch.
func (c *calculator) Calculate(branch string, head *object.Commit) (result, error) {
	c.logf("HEAD is %s on branch %q", short(head.Hash), branch)
	switch branch {
	case "main", "master":
		c.logf("branch classified as main (release)")
		cr, err := c.mainVersion(head)
		if err != nil {
			return result{}, err
		}
		c.logf("result: %s (release)", cr)
		return result{core: cr, isMain: true, branch: branch, headHash: head.Hash}, nil

	case "develop":
		c.logf("branch classified as develop")
		cr, n, err := c.developVersion(head)
		if err != nil {
			return result{}, err
		}
		c.logf("result: %s-alpha.%d", cr, n)
		return result{core: cr, prerelease: fmt.Sprintf("alpha.%d", n), branch: branch, headHash: head.Hash}, nil

	default:
		c.logf("branch classified as other (feature/bugfix/etc.)")
		cr, n, err := c.otherVersion(head, branch)
		if err != nil {
			return result{}, err
		}
		label := sanitizeLabel(branch)
		c.logf("sanitized branch label %q -> %q", branch, label)
		if label == "" {
			return result{}, fmt.Errorf("branch name %q produces an empty version label", branch)
		}
		c.logf("result: %s-%s.%d", cr, label, n)
		return result{core: cr, prerelease: fmt.Sprintf("%s.%d", label, n), branch: branch, headHash: head.Hash}, nil
	}
}
