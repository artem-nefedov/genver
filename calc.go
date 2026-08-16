package main

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// result is a computed version, split so the --format flag can render
// individual components (base, prerelease tail, major/minor/patch, branch).
type result struct {
	core       core
	prerelease string // "" on the main branch
	isMain     bool
	branch     string        // the branch the version was computed for
	headHash   plumbing.Hash // HEAD commit the version was computed for
	vPrefix    bool          // the boundary tag was "v"-prefixed; defaults output/tags to "v"
}

// version renders the full version string.
func (r result) version() (string, error) {
	return format(r.core, r.prerelease)
}

// boundary is a develop-line commit that starts a new "section": either the
// develop tip that a tagged main merge released, or the repository root.
type boundary struct {
	commit  *object.Commit
	core    core
	tagHash plumbing.Hash // the tagged commit this boundary came from (zero if untagged)
	vPrefix bool          // the boundary's release tag is "v"-prefixed
}

// calculator holds the state needed to compute a version.
type calculator struct {
	g          *repo
	boundaries []boundary
	boundaryAt map[plumbing.Hash]int           // boundary commit hash -> index into boundaries
	tagCore    map[plumbing.Hash]core          // commit hash -> core of a tag on it
	tagVPrefix map[plumbing.Hash]bool          // commit hash -> its release tag is "v"-prefixed
	refs       map[plumbing.Hash]prereleaseRef // commit hash -> prerelease reference tag
	conflicts  map[plumbing.Hash]error         // tagged commit hash -> ambiguity error (lazy)
	trace      io.Writer                       // nil disables tracing
}

func newCalculator(g *repo) (*calculator, error) {
	return newCalculatorTrace(g, nil)
}

// newCalculatorTrace builds a calculator that logs every calculation step to
// trace (unless trace is nil).
func newCalculatorTrace(g *repo, trace io.Writer) (*calculator, error) {
	c := &calculator{g: g, trace: trace}

	// Cache the tag maps once; they are consulted repeatedly. Release tags become
	// boundaries; prerelease reference tags (with a trailing counter) pin an
	// in-progress version; any other tag is ignored entirely and traced.
	tc, vpre, refs, conflicts, err := g.tagCores(func(name string, perr error) {
		c.logf("ignoring tag %q: %v", name, perr)
	})
	if err != nil {
		return nil, err
	}
	c.tagCore = tc
	c.tagVPrefix = vpre
	c.refs = refs
	c.conflicts = conflicts
	for h, pr := range refs {
		c.logf("prerelease reference tag on %s -> %s-%s.%d", short(h), pr.core, pr.label, pr.counter)
	}
	for h := range conflicts {
		c.logf("commit %s carries conflicting version tags (only an error if it is used)", short(h))
	}

	// developBoundaries populates c.boundaries and c.boundaryAt.
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
// avoiding a quadratic blowup on large, densely tagged repositories. When there
// is no tag at all, the pass covers the whole chain starting from the 0.1.0
// root, which is inherently the cost of an untagged repository.
//
// The repository root is always a boundary (core 0.1.0, or a tag on it).
// Boundaries are populated into c.boundaries / c.boundaryAt as the pass
// proceeds so that computing an untagged develop-release merge's core (via
// developVersion) can rely on the earlier boundaries already registered.
func (c *calculator) developBoundaries() ([]boundary, error) {
	c.boundaryAt = map[plumbing.Hash]int{}
	c.boundaries = nil
	add := func(bc *object.Commit, cr core, tagHash plumbing.Hash) {
		if _, seen := c.boundaryAt[bc.Hash]; seen {
			return
		}
		vpre := false
		if !tagHash.IsZero() {
			vpre = c.tagVPrefix[tagHash]
		}
		c.boundaryAt[bc.Hash] = len(c.boundaries)
		c.boundaries = append(c.boundaries, boundary{commit: bc, core: cr, tagHash: tagHash, vPrefix: vpre})
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
		add(bc, cr, h)
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
	add(root, core{0, 1, 0}, plumbing.ZeroHash)

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
			cur = cur.bump(max(bumpPatch, bumpFromMessage(cm.Message)))
			add(cm, cur, plumbing.ZeroHash)
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
			dc, _, _, derr := c.developVersion(p)
			if derr != nil {
				return nil, derr
			}
			cur = dc
		} else {
			// Direct merge of a non-develop branch: advance the core once, then
			// let any prerelease reference tag the merge carried in pin a higher
			// core.
			bump, berr := c.directMergeBump(cm)
			if berr != nil {
				return nil, berr
			}
			cur = cur.bump(bump)
			floor, ferr := c.directMergeRefFloor(cm)
			if ferr != nil {
				return nil, ferr
			}
			if less(cur, floor) {
				cur = floor
			}
		}
		add(p, cur, plumbing.ZeroHash)
	}

	return c.boundaries, nil
}

// isBoundary reports the boundary core if x is itself a release boundary.
func (c *calculator) isBoundary(x *object.Commit) (core, bool) {
	i, ok := c.boundaryAt[x.Hash]
	if !ok {
		return core{}, false
	}
	return c.boundaries[i].core, true
}

// boundaryConflictAt returns the ambiguity error for the boundary at commit hash
// h, if that boundary came from an ambiguous tagged commit. Used when a boundary
// commit is selected directly (e.g. develop HEAD is itself a boundary).
func (c *calculator) boundaryConflictAt(h plumbing.Hash) error {
	if i, ok := c.boundaryAt[h]; ok {
		return c.conflictAt(c.boundaries[i].tagHash)
	}
	return nil
}

// boundaryVPrefixAt reports whether the boundary at commit hash h came from a
// "v"-prefixed release tag. Used when a boundary commit is selected directly as
// the version's base, so the caller can default the output spelling to it.
func (c *calculator) boundaryVPrefixAt(h plumbing.Hash) bool {
	if i, ok := c.boundaryAt[h]; ok {
		return c.boundaries[i].vPrefix
	}
	return false
}

// conflictAt returns the ambiguity error for a tagged commit, or nil. It is
// called only when a commit's tag is actually selected for the answer, so a
// conflict on an irrelevant commit never surfaces.
func (c *calculator) conflictAt(h plumbing.Hash) error {
	if h.IsZero() {
		return nil
	}
	return c.conflicts[h]
}

// sectionScan holds the result of analysing a single develop "section": the
// commits reachable from the section's start but not from the release boundary
// it builds on (equivalent to `git rev-list base..start`).
type sectionScan struct {
	baseCore    core          // release core of the boundary the section builds on
	baseHash    plumbing.Hash // that boundary's commit hash
	baseVPrefix bool          // the base boundary's release tag is "v"-prefixed
	count       int           // number of commits in the section (base..start)
	bump        bumpKind      // strongest version bump those commits imply
	refFloor    core          // highest prerelease-reference core within the section (zero if none)
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
	// Walk the parent edges reachable from start exactly once. Which boundary is
	// the base and how many commits sit above it are then answered from this pool
	// in memory.
	pool, err := c.g.parentPool(start.Hash)
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
	var baseTagHash plumbing.Hash
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
			res.baseVPrefix = b.vPrefix
			baseTagHash = b.tagHash
			found = true
		}
	}
	if !found {
		return sectionScan{}, nil
	}
	// The selected boundary's tag is relevant to the answer: fail if ambiguous.
	if err := c.conflictAt(baseTagHash); err != nil {
		return sectionScan{}, err
	}

	// Count and bump over base..start: reachable from start, excluding the base
	// and everything reachable from it.
	exclude := ancestorHashesIn(res.baseHash, pool)
	count, bump, err := c.countAndBumpInPool(start, exclude, pool)
	if err != nil {
		return sectionScan{}, err
	}
	res.count, res.bump = count, bump

	// Highest prerelease-reference core within the section (base..start): a
	// reference tag carried in by a merged branch pins an in-progress core that
	// the section's release must be at least as high as.
	floor, err := c.refFloorInSection(start, exclude, pool)
	if err != nil {
		return sectionScan{}, err
	}
	res.refFloor = floor
	return res, nil
}

// refFloorInSection returns the highest prerelease-reference core among commits
// reachable from start but not in exclude (i.e. the section base..start). The
// zero core is returned when the section carries no reference tag. If the commit
// that supplies the winning floor is ambiguous (conflicting tags), an error is
// returned, since that reference is relevant to the answer.
func (c *calculator) refFloorInSection(start plumbing.Hash, exclude map[plumbing.Hash]bool, pool map[plumbing.Hash][]plumbing.Hash) (core, error) {
	var floor core
	var floorHash plumbing.Hash
	if len(c.refs) == 0 {
		return floor, nil
	}
	seen := map[plumbing.Hash]bool{}
	stack := []plumbing.Hash{start}
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] || exclude[h] {
			continue
		}
		seen[h] = true
		if pr, ok := c.refs[h]; ok && less(floor, pr.core) {
			floor = pr.core
			floorHash = h
		}
		for _, ph := range pool[h] {
			if !seen[ph] && !exclude[ph] {
				stack = append(stack, ph)
			}
		}
	}
	if err := c.conflictAt(floorHash); err != nil {
		return core{}, err
	}
	return floor, nil
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
			b := max(bumpPatch, bumpFromMessage(commit.Message))
			if isFeatureMerge(commit) {
				b = max(b, bumpMinor)
			}
			bump = max(bump, b)
		}
		for _, ph := range parents {
			if !seen[ph] && !exclude[ph] {
				stack = append(stack, ph)
			}
		}
	}
	return count, bump, nil
}

// developVersion computes the (core, counter, base-"v"-prefix) for a develop
// commit. The base-"v"-prefix reports whether the release boundary the version
// builds on is spelled with a leading "v", so the caller can default the output
// spelling to it.
func (c *calculator) developVersion(head *object.Commit) (core, int, bool, error) {
	// If HEAD is itself a release boundary, the current section is empty: keep
	// the released core and report the previous section's commit count.
	if bcore, ok := c.isBoundary(head); ok {
		c.logf("develop: HEAD %s is itself a release boundary (%s); section is empty", short(head.Hash), bcore)
		if err := c.boundaryConflictAt(head.Hash); err != nil {
			return core{}, 0, false, err
		}
		scan, err := c.scanSection(head, true)
		if err != nil {
			// No lower boundary (HEAD is the root boundary): the section that
			// ends here is empty, so the counter is 0.
			c.logf("develop: no lower boundary; counter = 0")
			return bcore, 0, false, nil
		}
		c.logf("develop: previous boundary %s; counter = %d; core = %s", short(scan.baseHash), scan.count, bcore)
		return bcore, scan.count, scan.baseVPrefix, nil
	}

	scan, err := c.scanSection(head, false)
	if err != nil {
		return core{}, 0, false, err
	}
	out := scan.baseCore.bump(scan.bump)
	if less(out, scan.refFloor) {
		c.logf("develop: reference floor %s raises core above computed %s", scan.refFloor, out)
		out = scan.refFloor
	}
	c.logf("develop: section starts at boundary %s (%s); bump = %s; %d commit(s); core %s -> %s",
		short(scan.baseHash), scan.baseCore, scan.bump, scan.count, scan.baseCore, out)
	return out, scan.count, scan.baseVPrefix, nil
}

// mainVersion computes the release core for a commit on the main branch, plus
// whether the base tag it builds on is "v"-prefixed (so the caller can default
// the output spelling to it).
func (c *calculator) mainVersion(head *object.Commit) (core, bool, error) {
	chain, err := c.g.firstParentChain(head)
	if err != nil {
		return core{}, false, err
	}

	// Find the nearest tagged commit on the first-parent chain.
	baseIdx := len(chain) - 1
	baseCore := core{0, 1, 0} // root default
	baseVPrefix := false      // no tag under the root: default stays bare
	for i, cm := range chain {
		if bcore, ok := c.tagCore[cm.Hash]; ok {
			// This tag is the selected base: fail if the commit is ambiguous.
			if err := c.conflictAt(cm.Hash); err != nil {
				return core{}, false, err
			}
			baseCore = bcore
			baseIdx = i
			baseVPrefix = c.tagVPrefix[cm.Hash]
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
			bump := max(bumpPatch, bumpFromMessage(cm.Message))
			next := cur.bump(bump)
			c.logf("main: direct commit %s -> %s bump: %s -> %s", short(cm.Hash), bump, cur, next)
			cur = next

		case c.isDevelopReleaseMerge(cm):
			// Release merge from develop: the release core is develop's core at
			// the merged tip (second parent).
			p, err := cm.Parent(1)
			if err != nil {
				return core{}, false, err
			}
			dc, _, _, err := c.developVersion(p)
			if err != nil {
				return core{}, false, err
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
				return core{}, false, err
			}
			next := cur.bump(bump)
			c.logf("main: direct merge of %q %s -> %s bump: %s -> %s", branch, short(cm.Hash), bump, cur, next)
			cur = next

			// A prerelease reference tag carried in by the merged branch pins the
			// release core when it is higher than the bumped core.
			floor, err := c.directMergeRefFloor(cm)
			if err != nil {
				return core{}, false, err
			}
			if less(cur, floor) {
				c.logf("main: reference floor %s raises core above %s", floor, cur)
				cur = floor
			}
		}
	}
	return cur, baseVPrefix, nil
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
	pool, err := c.g.parentPool(cm.Hash)
	if err != nil {
		return bumpNone, err
	}
	exclude := ancestorHashesIn(p0.Hash, pool)
	_, rangeBump, err := c.countAndBumpInPool(p1.Hash, exclude, pool)
	if err != nil {
		return bumpNone, err
	}
	return max(floor, rangeBump), nil
}

// directMergeRefFloor returns the highest prerelease-reference core among the
// commits a direct merge into main introduced (reachable from the merged tip,
// the second parent, but not from main's prior tip, the first parent). It is the
// zero core when the merged branch carried no reference tag. This lets a
// reference-tagged branch merged straight into main (the no-develop / hotfix
// flow) pin the release core exactly as it does through develop.
func (c *calculator) directMergeRefFloor(cm *object.Commit) (core, error) {
	if len(c.refs) == 0 {
		return core{}, nil
	}
	p0, err := cm.Parent(0)
	if err != nil {
		return core{}, err
	}
	p1, err := cm.Parent(1)
	if err != nil {
		return core{}, err
	}
	pool, err := c.g.parentPool(cm.Hash)
	if err != nil {
		return core{}, err
	}
	exclude := ancestorHashesIn(p0.Hash, pool)
	return c.refFloorInSection(p1.Hash, exclude, pool)
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

// forkBase returns the commit on the integration branch's first-parent chain
// (its permanent mainline) that the branch was cut from: the newest such commit
// that is an ancestor of head.
//
// This is the branch's fork point, and it is stable regardless of whether the
// branch has since been merged back into the integration branch. A plain
// merge-base collapses onto head once the branch is merged (its own commits then
// belong to the integration branch), which would erase the branch's increment;
// walking the integration mainline instead ignores the merge commit — head is
// its second parent, not an ancestor of it — and finds the true fork point. When
// the branch has advanced past that fork point (e.g. develop was later merged
// back into it), the newest chain commit reachable from head is still the fork
// point, so the branch's own commits above it are counted correctly.
func (c *calculator) forkBase(head, integrationTip *object.Commit) (*object.Commit, error) {
	chain, err := c.g.firstParentChain(integrationTip) // newest first
	if err != nil {
		return nil, err
	}
	pool, err := c.g.parentPool(head.Hash)
	if err != nil {
		return nil, err
	}
	for _, cm := range chain {
		if _, reachable := pool[cm.Hash]; reachable {
			return cm, nil
		}
	}
	// No mainline commit is an ancestor of head (unrelated histories): fall back
	// to the plain merge-base so the caller still has a base to build on.
	return c.g.mergeBase(head, integrationTip)
}

// otherVersion computes the version for a non-main, non-develop branch. It
// returns the core, the prerelease label to use (empty means "use the branch's
// sanitized name"), and the counter.
//
// The branch is versioned relative to the integration branch it was cut from:
// "develop" when that permanent branch exists, otherwise "main" (the flow where
// short-lived branches are created directly off main). In the main-based flow
// every main commit above the latest tag is itself a release boundary, so the
// fork point on main is a boundary and the "section" below the branch carries
// no accumulated bump — the branch builds straight on main's release core.
//
// A prerelease reference tag on one of the branch's own commits (e.g.
// "4.5.6-foobar-x.3") can override all of this: when its core is higher than the
// normally-computed core, calculation continues from the tag — its core and
// label are used, and the counter continues from the tag's counter plus the
// commits made after it. When the computed core is higher, the tag is ignored.
func (c *calculator) otherVersion(head *object.Commit, branch string) (core, string, int, bool, error) {
	integrationTip, integration, err := c.integrationBranch()
	if err != nil {
		return core{}, "", 0, false, err
	}
	mb, err := c.forkBase(head, integrationTip)
	if err != nil {
		return core{}, "", 0, false, err
	}
	c.logf("other: fork point on %s is %s", integration, short(mb.Hash))

	// The fork point is an ancestor of head, so one parent pool walked from head
	// holds everything both the develop-section scan and the branch count need.
	pool, err := c.g.parentPool(head.Hash)
	if err != nil {
		return core{}, "", 0, false, err
	}

	// Determine the release core the integration section builds on, and the bump
	// that section has accumulated so far (at the fork point). A single
	// section-scan handles both the boundary and non-boundary fork-point cases.
	var belowCore core
	baseVPrefix := false
	sectionBump := bumpNone
	if bcore, ok := c.isBoundary(mb); ok {
		if err := c.boundaryConflictAt(mb.Hash); err != nil {
			return core{}, "", 0, false, err
		}
		belowCore = bcore
		baseVPrefix = c.boundaryVPrefixAt(mb.Hash)
		c.logf("other: fork point is a release boundary (%s); %s section bump = none", bcore, integration)
	} else {
		scan, err := c.scanSectionInPool(mb.Hash, false, pool)
		if err != nil {
			return core{}, "", 0, false, err
		}
		if scan.baseHash.IsZero() {
			return core{}, "", 0, false, fmt.Errorf("no release boundary found for commit %s", mb.Hash)
		}
		belowCore = scan.baseCore
		baseVPrefix = scan.baseVPrefix
		sectionBump = scan.bump
		c.logf("other: %s section starts at %s (%s); %s section bump = %s", integration, short(scan.baseHash), scan.baseCore, integration, sectionBump)
	}

	// The branch's own commits (reachable from head but not from the fork point)
	// contribute at least a patch bump. Exclude everything reachable from the
	// fork point so the count matches `git rev-list mb..head`.
	mbSet := ancestorHashesIn(mb.Hash, pool)
	n, branchBump, err := c.countAndBumpInPool(head.Hash, mbSet, pool)
	if err != nil {
		return core{}, "", 0, false, err
	}
	c.logf("other: branch's own commits bump = %s", branchBump)
	eff := max(sectionBump, branchBump)
	if isFeatureBranch(branch) {
		eff = max(eff, bumpMinor) // feature increment takes precedence
		c.logf("other: feature branch -> effective bump forced to at least minor")
	}

	out := belowCore.bump(eff)
	c.logf("other: effective bump = %s; %d commit(s) since fork point; core %s -> %s", eff, n, belowCore, out)

	// A prerelease reference tag among the branch's own commits (mb..head) can
	// take over when its core is at least as high as the computed core.
	if ref, refHash, after, ok, rerr := c.nearestRef(head.Hash, mbSet, pool); rerr != nil {
		return core{}, "", 0, false, rerr
	} else if ok {
		if less(out, ref.core) || out == ref.core {
			counter := ref.counter + after
			c.logf("other: reference tag on %s (%s-%s.%d) wins; counter = %d + %d after = %d",
				short(refHash), ref.core, ref.label, ref.counter, ref.counter, after, counter)
			return ref.core, ref.label, counter, baseVPrefix, nil
		}
		c.logf("other: reference tag on %s (%s) is lower than computed core %s; ignored",
			short(refHash), ref.core, out)
	}

	return out, "", n, baseVPrefix, nil
}

// nearestRef finds the prerelease reference tag nearest to head among the
// commits reachable from head but not in exclude (i.e. the branch's own
// commits). "Nearest" is by graph distance from head. It returns the reference,
// the commit it sits on, the number of the branch's own commits strictly after
// that commit (which continue the counter), and whether one was found.
func (c *calculator) nearestRef(head plumbing.Hash, exclude map[plumbing.Hash]bool, pool map[plumbing.Hash][]plumbing.Hash) (prereleaseRef, plumbing.Hash, int, bool, error) {
	if len(c.refs) == 0 {
		return prereleaseRef{}, plumbing.ZeroHash, 0, false, nil
	}
	// BFS from head over the branch's own commits, recording distance.
	dist := map[plumbing.Hash]int{head: 0}
	queue := []plumbing.Hash{head}
	bestHash := plumbing.ZeroHash
	bestDist := -1
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if _, ok := c.refs[h]; ok {
			if bestDist < 0 || dist[h] < bestDist {
				bestDist = dist[h]
				bestHash = h
			}
		}
		for _, ph := range pool[h] {
			if exclude[ph] {
				continue
			}
			if _, seen := dist[ph]; seen {
				continue
			}
			dist[ph] = dist[h] + 1
			queue = append(queue, ph)
		}
	}
	if bestDist < 0 {
		return prereleaseRef{}, plumbing.ZeroHash, 0, false, nil
	}
	// The selected reference tag is relevant to the answer: fail if ambiguous.
	if err := c.conflictAt(bestHash); err != nil {
		return prereleaseRef{}, plumbing.ZeroHash, 0, false, err
	}
	// Count the branch's own commits strictly after (i.e. that reach) the
	// reference commit: everything from head excluding the reference commit and
	// its ancestors.
	afterExclude := ancestorHashesIn(bestHash, pool)
	after, _, err := c.countAndBumpInPool(head, afterExclude, pool)
	if err != nil {
		return prereleaseRef{}, plumbing.ZeroHash, 0, false, err
	}
	return c.refs[bestHash], bestHash, after, true, nil
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
		cr, vpre, err := c.mainVersion(head)
		if err != nil {
			return result{}, err
		}
		c.logf("result: %s (release)", cr)
		return result{core: cr, isMain: true, branch: branch, headHash: head.Hash, vPrefix: vpre}, nil

	case "develop":
		c.logf("branch classified as develop")
		cr, n, vpre, err := c.developVersion(head)
		if err != nil {
			return result{}, err
		}
		c.logf("result: %s-alpha.%d", cr, n)
		return result{core: cr, prerelease: fmt.Sprintf("alpha.%d", n), branch: branch, headHash: head.Hash, vPrefix: vpre}, nil

	default:
		c.logf("branch classified as other (feature/bugfix/etc.)")
		cr, labelOverride, n, vpre, err := c.otherVersion(head, branch)
		if err != nil {
			return result{}, err
		}
		label := labelOverride
		if label == "" {
			label = sanitizeLabel(branch)
			c.logf("sanitized branch label %q -> %q", branch, label)
		} else {
			c.logf("using reference-tag label %q", label)
		}
		if label == "" {
			return result{}, fmt.Errorf("branch name %q produces an empty version label", branch)
		}
		c.logf("result: %s-%s.%d", cr, label, n)
		return result{core: cr, prerelease: fmt.Sprintf("%s.%d", label, n), branch: branch, headHash: head.Hash, vPrefix: vpre}, nil
	}
}
