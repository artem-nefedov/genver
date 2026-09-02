package main

import (
	"fmt"
	"io"
	"slices"
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
	msgBumps   map[plumbing.Hash]bumpKind      // commit hash -> parsed "+semver:" bump (memo)
	trace      io.Writer                       // nil disables tracing
}

// msgBump returns the "+semver:" bump encoded in a commit's message, memoized by
// commit hash. The same commits are visited by several walks (boundary
// discovery, ceiling computation, and the main range scan), so caching the
// parse avoids re-running the directive regex on identical messages.
func (c *calculator) msgBump(commit *object.Commit) bumpKind {
	if b, ok := c.msgBumps[commit.Hash]; ok {
		return b
	}
	b := bumpFromMessage(commit.Message)
	c.msgBumps[commit.Hash] = b
	return b
}

func newCalculator(g *repo) (*calculator, error) {
	return newCalculatorTrace(g, nil)
}

// newCalculatorTrace builds a calculator that logs every calculation step to
// trace (unless trace is nil).
func newCalculatorTrace(g *repo, trace io.Writer) (*calculator, error) {
	c := &calculator{g: g, trace: trace, msgBumps: map[plumbing.Hash]bumpKind{}}

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
	tracef(c.trace, format, args...)
}

// tracef writes a single timestamped trace line to w (a no-op when w is nil).
// It is the shared sink for both calculator and repo tracing, so debug output
// has a uniform format regardless of which layer emitted it.
func tracef(w io.Writer, format string, args ...any) {
	if w == nil {
		return
	}
	ts := time.Now().Format("2006-01-02 15:04:05.000000000")
	fmt.Fprintf(w, ts+": "+format+"\n", args...)
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
// core computation (developVersion / directMergeCore, each a history walk) runs
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
			cur = cur.bump(max(bumpPatch, c.msgBump(cm)))
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
			// merged tip, built on the boundaries registered so far. A
			// "+semver:" directive on the release-merge commit itself raises the
			// bump level (via max) just as it would on any other commit.
			dc, derr := c.developReleaseCore(p, c.msgBump(cm))
			if derr != nil {
				return nil, derr
			}
			cur = dc
		} else {
			// Direct merge of a non-develop branch: advance the core once,
			// honoring any reference tag the merge carried in (anchor up/down).
			dc, berr := c.directMergeCore(cm, cur)
			if berr != nil {
				return nil, berr
			}
			cur = dc
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

// mergeBoundary reports the release core a merge commit produced, if x is a
// merge whose boundary was registered at its released tip (its second parent),
// as developBoundaries does for every merge on the mainline. This is distinct
// from isBoundary, which only recognizes the boundary commit itself: a merge
// commit is never its own boundary (the boundary sits on its second parent), so
// a branch that forks directly from such a merge would otherwise fail to see
// that the merge's core is already established and would re-apply the merge's
// bump on top of it. It also reports whether that boundary's tag is
// "v"-prefixed, mirroring boundaryVPrefixAt.
func (c *calculator) mergeBoundary(x *object.Commit) (core, bool, bool, error) {
	if x.NumParents() < 2 {
		return core{}, false, false, nil
	}
	p, err := x.Parent(1)
	if err != nil {
		return core{}, false, false, err
	}
	i, ok := c.boundaryAt[p.Hash]
	if !ok {
		return core{}, false, false, nil
	}
	b := &c.boundaries[i]
	return b.core, true, b.vPrefix, nil
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

	// hasRef reports whether a prerelease reference tag was found in the section
	// (base..start). When set, refAnchor is the core that tag anchors the
	// section to: the tag's core raised by the bump of the commits strictly
	// after the tagged commit (a "+semver:" directive or feature merge landing
	// after the tag can still lift the anchor). Unlike refFloor, which can only
	// raise the computed core, refAnchor REPLACES the section's own bump (so a
	// reference tag can pull the core back down), bounded from below by baseCore
	// (a reference tag can never sink the section below the release it builds on).
	// refBase is the tag's OWN core before any after-tag lift; the anchor is only
	// relevant when refBase (not the lifted refAnchor) is at least as high as
	// baseCore, so a stale tag below the current release line cannot be lifted
	// above it and hijack the section.
	hasRef    bool
	refAnchor core
	refBase   core
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

	// Analyse base..start in a single walk: count, bump, and any reference tag.
	exclude := ancestorHashesIn(res.baseHash, pool)
	rs, err := c.scanRange(start, exclude, pool)
	if err != nil {
		return sectionScan{}, err
	}
	res.count, res.bump = rs.count, rs.bump
	res.refFloor = rs.floorRef

	// A reference tag also ANCHORS the section: the tag nearest the section tip
	// pins the core to its own core, raised only by EXPLICIT bumps on commits
	// after the tag, letting a reference tag pull the core back down as well as
	// up. The anchor is bounded from below by the base core (applied by the
	// consumers via max(baseCore, refAnchor)). The after-tag bump needs its own
	// walk because its range (tag..start) differs from the section's.
	if rs.hasRef {
		lift, aerr := c.anchorLiftBump(start, rs.nearestHash, pool)
		if aerr != nil {
			return sectionScan{}, aerr
		}
		res.hasRef = true
		res.refBase = rs.nearestRef.core
		res.refAnchor = rs.nearestRef.core.bump(lift)
	}
	return res, nil
}

// rangeScan is the full analysis of a commit range base..start, produced by a
// single walk (scanRange). It answers every question the version calculation
// asks about a range so that no range is ever walked more than once.
type rangeScan struct {
	count        int           // number of commits in the range
	bump         bumpKind      // strongest bump WITH a patch floor (ordinary commit -> patch)
	explicitBump bumpKind      // strongest EXPLICIT bump (no floor): "+semver:"/feature merge only
	hasRef       bool          // whether a prerelease reference tag was found in the range
	nearestRef   prereleaseRef // the reference tag nearest the range tip (by graph distance)
	nearestHash  plumbing.Hash // the commit the nearest reference sits on
	floorRef     core          // highest prerelease-reference core in the range (raise-only floor)
	floorHash    plumbing.Hash // the commit supplying floorRef
}

// scanRange walks the commits reachable from start but not in exclude exactly
// ONCE, computing the commit count, the patch-floored bump, the floorless
// explicit bump, and both the nearest-to-tip and highest-core prerelease
// reference in the range. Consolidating these into one pass avoids re-walking
// the same range for each quantity. Reference-tag bookkeeping is skipped
// entirely when the repository has no reference tags. The nearest reference wins
// ties in distance toward the higher core so the anchor is deterministic; both
// the nearest and highest reference commits are checked for tag conflicts, since
// either may be relevant to the answer.
//
// A merge commit carrying an explicit "+semver:" directive imposes that level as
// a CEILING on everything it introduced (the commits reachable from its second
// parent but not its first): an inner "+semver: major" under a "+semver: minor"
// merge is capped to minor, and a "+semver: patch" merge suppresses inner minor
// bumps, feature merges, and reference tags alike. Ceilings compose through
// nesting (the lowest wins along a path) but a commit also reachable by an
// independent, un-capped path keeps its full weight. The commit COUNT is never
// affected — every commit in the range is counted once.
func (c *calculator) scanRange(start plumbing.Hash, exclude map[plumbing.Hash]bool, pool map[plumbing.Hash][]plumbing.Hash) (rangeScan, error) {
	ceilings, err := c.computeCeilings(start, exclude, pool)
	if err != nil {
		return rangeScan{}, err
	}
	var res rangeScan
	haveRefs := len(c.refs) > 0
	// A distance map doubles as the "seen" set; the BFS order it imposes is only
	// needed for the nearest-reference tie-break but is harmless otherwise.
	dist := map[plumbing.Hash]int{start: 0}
	queue := []plumbing.Hash{start}
	bestDist := -1
	for len(queue) > 0 {
		h := queue[0]
		queue = queue[1:]
		if exclude[h] {
			continue
		}
		parents, ok := pool[h]
		if !ok {
			continue
		}
		res.count++
		ceiling := bumpMajor
		if cv, ok := ceilings[h]; ok {
			ceiling = cv
		}
		// Decode the commit only while it can still change a bump; once both
		// bumps have peaked at major there is nothing left to learn from messages.
		if res.bump < bumpMajor || res.explicitBump < bumpMajor {
			commit, err := c.g.r.CommitObject(h)
			if err != nil {
				return rangeScan{}, err
			}
			explicit, berr := c.explicitCommitBump(commit)
			if berr != nil {
				return rangeScan{}, berr
			}
			explicit = min(explicit, ceiling) // a capping merge limits its content
			res.explicitBump = max(res.explicitBump, explicit)
			res.bump = max(res.bump, max(bumpPatch, explicit))
		}
		// A patch ceiling suppresses reference tags too (the "+semver: patch"
		// merge overrides even a reference-tag anchor the merged branch carried).
		if haveRefs && ceiling > bumpPatch {
			if pr, ok := c.refs[h]; ok {
				d := dist[h]
				if bestDist < 0 || d < bestDist || (d == bestDist && less(res.nearestRef.core, pr.core)) {
					bestDist = d
					res.nearestRef = pr
					res.nearestHash = h
					res.hasRef = true
				}
				if less(res.floorRef, pr.core) {
					res.floorRef = pr.core
					res.floorHash = h
				}
			}
		}
		for _, ph := range parents {
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
	if res.hasRef {
		if err := c.conflictAt(res.nearestHash); err != nil {
			return rangeScan{}, err
		}
		if res.floorHash != res.nearestHash {
			if err := c.conflictAt(res.floorHash); err != nil {
				return rangeScan{}, err
			}
		}
	}
	return res, nil
}

// anchorLiftBump returns the explicit bump that lifts a reference anchor: the
// strongest EXPLICIT signal ("+semver:" directive or feature merge) on commits
// that are genuine DESCENDANTS of the anchor commit anchorHash — i.e. commits
// that build ON TOP of the tag, reachable from start with the anchor among their
// ancestors. Commits reachable from start but merely PARALLEL to the anchor
// (independent work on another line, e.g. feature merges that landed on develop
// while the tagged branch was being developed) are NOT "after the tag" and do
// not lift it. A feature merge contributes minor only if the branch it
// integrates itself resolves to minor-or-higher; a feature branch that a
// reference tag capped to patch (with nothing restoring it) contributes only
// patch, exactly like a bugfix merge — so merging such a branch does not undo
// its own patch decision. An explicit "+semver:" on any commit still applies.
//
// The merge that INTEGRATES the anchored branch itself does not re-apply its
// implicit feature-minor: when the anchor tag sits on the FIRST-PARENT chain of
// the merge's merged-in side (i.e. the tag is that branch's own final version,
// as with a "1.2.3-foo.5" tag on a feature branch merged into develop), the
// feature-merge weight is the same work the anchor already reflects. Counting it
// would double-apply the bump and jump 1.2.3 -> 1.3.0. Only the implicit
// feature-minor is suppressed for such a merge: an explicit "+semver:" directive
// on the merge message is a deliberate override and still lifts, and a nested
// feature merge that brought in an independent branch (the anchor is NOT on the
// merged side's first-parent chain) keeps its full weight.
func (c *calculator) anchorLiftBump(start, anchorHash plumbing.Hash, pool map[plumbing.Hash][]plumbing.Hash) (bumpKind, error) {
	afterExclude := ancestorHashesIn(anchorHash, pool)
	// desc answers "is the anchor an ancestor of h?" (i.e. h builds on the tag),
	// memoized once and reused by both the lift walk and integratesAnchor so the
	// anchor's ancestor set is never recomputed per merge.
	desc := newAncestorMemo(anchorHash, pool)
	seen := map[plumbing.Hash]bool{}
	stack := []plumbing.Hash{start}
	bump := bumpNone
	for len(stack) > 0 {
		h := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[h] || afterExclude[h] {
			continue
		}
		seen[h] = true
		parents, ok := pool[h]
		if !ok {
			continue
		}
		// Only commits built on top of the tag can lift it; parallel independent
		// work does not.
		if bump < bumpMajor && desc.reaches(h) {
			commit, err := c.g.r.CommitObject(h)
			if err != nil {
				return bumpNone, err
			}
			var b bumpKind
			if len(parents) >= 2 && c.integratesAnchor(parents, anchorHash, desc, pool) {
				// This merge integrates the anchored branch directly: honor its
				// explicit "+semver:" directive but not its implicit feature-minor.
				b = c.msgBump(commit)
			} else {
				b, err = c.explicitCommitBump(commit)
				if err != nil {
					return bumpNone, err
				}
			}
			bump = max(bump, b)
		}
		for _, ph := range parents {
			if !seen[ph] && !afterExclude[ph] {
				stack = append(stack, ph)
			}
		}
	}
	return bump, nil
}

// ancestorMemo answers, lazily and with memoization, whether a fixed target
// commit is an ancestor of a queried commit within a parent pool. Reusing a
// single memo across many queries avoids recomputing (and re-allocating) the
// target's full ancestor set per query.
type ancestorMemo struct {
	target plumbing.Hash
	pool   map[plumbing.Hash][]plumbing.Hash
	cache  map[plumbing.Hash]bool
}

func newAncestorMemo(target plumbing.Hash, pool map[plumbing.Hash][]plumbing.Hash) *ancestorMemo {
	return &ancestorMemo{target: target, pool: pool, cache: map[plumbing.Hash]bool{}}
}

// reaches reports whether the memo's target is h itself or an ancestor of h.
func (m *ancestorMemo) reaches(h plumbing.Hash) bool {
	if h == m.target {
		return true
	}
	if v, ok := m.cache[h]; ok {
		return v
	}
	m.cache[h] = false // break potential cycles (a DAG has none, but be safe)
	res := slices.ContainsFunc(m.pool[h], m.reaches)
	m.cache[h] = res
	return res
}

// integratesAnchor reports whether a merge with the given parents directly
// integrates the anchored branch: the anchor commit is on the FIRST-PARENT chain
// of one of the merged-in parents (second or beyond) but not reachable from the
// first parent. That means the anchor tag is the merged branch's own final
// version, so the merge's implicit feature-minor must not re-lift it. The shared
// ancestor memo answers the first-parent reachability test without allocating a
// fresh ancestor set.
func (c *calculator) integratesAnchor(parents []plumbing.Hash, anchorHash plumbing.Hash, desc *ancestorMemo, pool map[plumbing.Hash][]plumbing.Hash) bool {
	if desc.reaches(parents[0]) {
		return false // anchor also reachable via the first parent: mainline anchor
	}
	for _, ph := range parents[1:] {
		if firstParentChainContains(ph, anchorHash, pool) {
			return true
		}
	}
	return false
}

// firstParentChainContains walks the first-parent chain from start and reports
// whether it reaches target.
func firstParentChainContains(start, target plumbing.Hash, pool map[plumbing.Hash][]plumbing.Hash) bool {
	h := start
	for {
		if h == target {
			return true
		}
		parents, ok := pool[h]
		if !ok || len(parents) == 0 {
			return false
		}
		h = parents[0]
	}
}

// explicitCommitBump is the strongest EXPLICIT bump a single commit contributes:
// its own "+semver:" directive, combined with — for a feature merge — the bump
// the merged-in feature branch actually resolves to (which a reference tag on
// that branch's tip may have capped to patch). No patch floor is applied here;
// an ordinary commit contributes bumpNone.
func (c *calculator) explicitCommitBump(commit *object.Commit) (bumpKind, error) {
	b := c.msgBump(commit)
	if isFeatureMerge(commit) {
		fb, err := c.featureMergeBump(commit)
		if err != nil {
			return bumpNone, err
		}
		b = max(b, fb)
	}
	return b, nil
}

// featureMergeBump returns the EXPLICIT bump a feature merge commit contributes,
// honoring a reference tag on the merged-in branch. Normally a feature merge is
// an explicit minor. But if the merged-in branch was capped to patch by a
// reference tag on its tip (the merge's second parent), the merge inherits that
// patch verdict and behaves like an ordinary/bugfix merge: it contributes NO
// explicit bump (bumpNone), so it neither lifts a reference anchor nor exceeds
// the patch floor applied elsewhere. The merge commit's own "+semver:" directive
// (handled by the caller) still applies on top. A tag deeper inside the merged
// branch does not cap it: the branch's own later work reasserts the feature
// minor.
func (c *calculator) featureMergeBump(m *object.Commit) (bumpKind, error) {
	if !isFeatureMerge(m) {
		return bumpNone, nil
	}
	// A "+semver: patch" on the merge caps it: the feature-minor is suppressed
	// (the caller still applies the patch via bumpFromMessage), regardless of a
	// reference-tag anchor on the merged branch.
	if c.msgBump(m) == bumpPatch {
		return bumpNone, nil
	}
	if len(c.refs) == 0 || m.NumParents() < 2 {
		return bumpMinor, nil
	}
	tip := m.ParentHashes[1]
	if _, ok := c.refs[tip]; !ok {
		return bumpMinor, nil // no anchor on the merged tip: full feature minor
	}
	if err := c.conflictAt(tip); err != nil {
		return bumpNone, err
	}
	// The tag is on the merged tip, so nothing inside the branch is after it to
	// restore the minor: the branch is capped. Its feature-ness contributes no
	// explicit bump (it is treated like a bugfix merge).
	return bumpNone, nil
}

// computeCeilings assigns each commit in the range base..start (reachable from
// start, not in exclude) the bump CEILING that applies to it. A commit with no
// entry is uncapped (ceiling bumpMajor). A merge commit carrying an explicit
// "+semver:" directive d caps every commit it introduced — those reachable from
// its second parent but not from its first — at min(d, the merge's own ceiling),
// composing through nested capping merges. A commit reachable by an independent
// path that is not under a capping merge keeps its full weight: the recorded
// ceiling is the MAXIMUM (least restrictive) over all paths that reach it.
func (c *calculator) computeCeilings(start plumbing.Hash, exclude map[plumbing.Hash]bool, pool map[plumbing.Hash][]plumbing.Hash) (map[plumbing.Hash]bumpKind, error) {
	ceilings := map[plumbing.Hash]bumpKind{}
	// visited records the strongest ceiling a commit has been reached with, so a
	// commit is re-expanded only when found via a less restrictive path.
	visited := map[plumbing.Hash]bumpKind{}
	type item struct {
		h       plumbing.Hash
		ceiling bumpKind
	}
	stack := []item{{start, bumpMajor}}
	for len(stack) > 0 {
		it := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if exclude[it.h] {
			continue
		}
		if prev, ok := visited[it.h]; ok && prev >= it.ceiling {
			continue // already reached with an equal-or-less-restrictive ceiling
		}
		visited[it.h] = it.ceiling
		if it.ceiling < bumpMajor {
			// Record the least restrictive ceiling seen so far for this commit.
			if cur, ok := ceilings[it.h]; !ok || it.ceiling > cur {
				ceilings[it.h] = it.ceiling
			}
		}
		parents, ok := pool[it.h]
		if !ok {
			continue
		}
		// Does this commit impose a lower ceiling on the branch it merged in?
		introducedCeiling := it.ceiling
		if len(parents) >= 2 {
			commit, err := c.g.r.CommitObject(it.h)
			if err != nil {
				return nil, err
			}
			if d := c.msgBump(commit); d != bumpNone {
				introducedCeiling = min(introducedCeiling, d)
			}
		}
		for i, ph := range parents {
			if exclude[ph] {
				continue
			}
			// The first parent stays on the current line (outer ceiling); the
			// merged-in parents (second and beyond) inherit the merge's ceiling.
			ceiling := it.ceiling
			if i > 0 {
				ceiling = introducedCeiling
			}
			stack = append(stack, item{ph, ceiling})
		}
	}
	return ceilings, nil
}

// sectionCore reduces a completed section scan to its release core, applying
// prerelease-reference semantics. extraBump is an additional bump that applies
// "after" the whole section (e.g. a "+semver:" directive on a release-merge
// commit that sits above the section); it raises both the plain computed bump
// and, being after every tagged commit, the reference anchor. When the section
// carries a reference tag whose anchor core is at least as high as the base
// release, the anchor REPLACES the section's own bump (so a reference tag can
// pull the core back down as well as up); otherwise the core is the base release
// raised by the section's bump, then lifted by any (raise-only) reference floor.
// baseCore always bounds the result from below: a reference tag can never sink
// the section beneath the release it builds on.
func (s sectionScan) core(extraBump bumpKind) core {
	if s.hasRef && !less(s.refBase, s.baseCore) {
		// The tag's OWN core is at or above the base release, so it anchors the
		// section (a stale tag below the current release line is ignored here so
		// an after-tag lift cannot raise it above the boundary and hijack the
		// section).
		return s.refAnchor.bump(extraBump)
	}
	out := s.baseCore.bump(max(s.bump, extraBump))
	if less(out, s.refFloor) {
		out = s.refFloor
	}
	return out
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
	out := scan.core(bumpNone)
	c.logf("develop: section starts at boundary %s (%s); bump = %s; %d commit(s); core %s -> %s",
		short(scan.baseHash), scan.baseCore, scan.bump, scan.count, scan.baseCore, out)
	return out, scan.count, scan.baseVPrefix, nil
}

// developReleaseCore returns the release core for a develop tip being merged
// into main. extraBump is the "+semver:" directive on the release-merge commit
// itself: as for every merge commit, an explicit directive is the final
// authority, forcing the release to exactly baseCore.bump(directive) — it can
// cap the release DOWN (a "+semver: patch" release merge yields a patch even if
// develop accumulated a minor) as well as raise it. With no directive the
// release publishes develop's accumulated core unchanged.
func (c *calculator) developReleaseCore(tip *object.Commit, extraBump bumpKind) (core, error) {
	if extraBump == bumpNone {
		dc, _, _, err := c.developVersion(tip)
		return dc, err
	}
	if bcore, ok := c.isBoundary(tip); ok {
		if err := c.boundaryConflictAt(tip.Hash); err != nil {
			return core{}, err
		}
		// The release core is already pinned at this tip (by a tag, or by the
		// boundary the release merge registered in the discovery pass). The
		// directive was already applied when that core was computed; return it.
		return bcore, nil
	}
	scan, err := c.scanSection(tip, false)
	if err != nil {
		return core{}, err
	}
	out := scan.baseCore.bump(extraBump) // directive is exact over the release base
	c.logf("develop: release-merge +semver:%s exact; core %s -> %s", extraBump, scan.baseCore, out)
	return out, nil
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
			bump := max(bumpPatch, c.msgBump(cm))
			next := cur.bump(bump)
			c.logf("main: direct commit %s -> %s bump: %s -> %s", short(cm.Hash), bump, cur, next)
			cur = next

		case c.isDevelopReleaseMerge(cm):
			// Release merge from develop: the release core is develop's core at
			// the merged tip (second parent). A "+semver:" directive on the
			// release-merge commit itself raises the bump level (via max).
			p, err := cm.Parent(1)
			if err != nil {
				return core{}, false, err
			}
			dc, err := c.developReleaseCore(p, c.msgBump(cm))
			if err != nil {
				return core{}, false, err
			}
			c.logf("main: release merge %s -> core from develop tip = %s", short(cm.Hash), dc)
			cur = dc

		default:
			// Direct merge of a non-develop branch into main (a hotfix-style
			// flow). A feature-branch merge bumps minor; any other branch bumps
			// patch. Either floor is raised when a merged-in commit requests a
			// stronger bump, and a reference tag the merge carried in can anchor
			// the core (up or down). The whole merge advances the core once.
			branch := mergedBranchName(cm)
			next, err := c.directMergeCore(cm, cur)
			if err != nil {
				return core{}, false, err
			}
			c.logf("main: direct merge of %q %s -> %s -> %s", branch, short(cm.Hash), cur, next)
			cur = next
		}
	}
	return cur, baseVPrefix, nil
}

// isDevelopReleaseMerge reports whether a merge commit on main is a release
// merge from the develop integration branch (as opposed to a direct merge of a
// feature/other branch into main). An unrecognized merge message defaults to a
// release merge, preserving the historical "every main merge releases develop"
// behavior for repositories whose merge commits genver cannot attribute.
func (c *calculator) isDevelopReleaseMerge(cm *object.Commit) bool {
	name := mergedBranchName(cm)
	return name == "" || name == "develop"
}

// directMergeCore computes the release core after a direct merge of a
// non-develop branch into main, building on the running main core base. The
// merge advances the core exactly once: a feature-branch merge floors at minor,
// any other branch at patch, and a "+semver:" directive on the merge commit or
// on any introduced commit can raise it further. A prerelease reference tag the
// merge carried in anchors the core to the tag's core; that anchor is still
// raised by explicit signals ABOVE the tag — a "+semver:" directive or a feature
// merge, which carry equal weight — so a feature merge whose branch tip is
// tagged still applies its minor on top of the anchor. Placing the tag on the
// merge commit itself (nothing after it) reverts the merge's own bump. The
// anchor can pull the result DOWN as well as up, but never below base, the
// release main already sits at. The commits considered are those reachable from
// the merged tip (second parent) but not from main's prior tip (first parent).
func (c *calculator) directMergeCore(cm *object.Commit, base core) (core, error) {
	// A "+semver: patch" directive on the merge commit (with no stronger
	// directive in the same message) caps the merge at a plain patch bump,
	// overriding the automatic feature-minor, any inner "+semver:" bumps, and
	// even a reference-tag anchor the merged branch carried. The merge message is
	// the final authority on the resulting bump.
	if d := c.msgBump(cm); d != bumpNone {
		next := base.bump(d)
		c.logf("main: direct merge %s has +semver:%s -> %s -> %s", short(cm.Hash), d, base, next)
		return next, nil
	}

	// No directive on the merge: a feature merge is worth minor only if the
	// merged-in branch actually resolves to minor (a reference tag on that branch
	// may have capped it to patch); any other branch floors at patch.
	featureBump, err := c.featureMergeBump(cm)
	if err != nil {
		return core{}, err
	}
	mergeBump := max(bumpPatch, featureBump)

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
	rs, err := c.scanRange(p1.Hash, exclude, pool)
	if err != nil {
		return core{}, err
	}

	scan := sectionScan{baseCore: base, bump: max(mergeBump, rs.bump)}
	// The merge commit itself sits above every introduced commit, so a reference
	// tag on it is the newest anchor. Being AT the merge, the merge's own signal
	// (feature-minor or a "+semver:" directive) is part of the tagged commit and
	// does not lift the anchor — this is how a feature merge's bump is reverted.
	if selfRef, ok := c.refs[cm.Hash]; ok {
		if err := c.conflictAt(cm.Hash); err != nil {
			return core{}, err
		}
		scan.hasRef = true
		scan.refAnchor = selfRef.core
		scan.refBase = selfRef.core
		scan.refFloor = selfRef.core
		return scan.core(bumpNone), nil
	}
	if rs.hasRef {
		lift, aerr := c.anchorLiftBump(p1.Hash, rs.nearestHash, pool)
		if aerr != nil {
			return core{}, aerr
		}
		scan.hasRef = true
		scan.refAnchor = rs.nearestRef.core.bump(lift)
		scan.refBase = rs.nearestRef.core
		scan.refFloor = rs.nearestRef.core
	}
	return scan.core(bumpNone), nil
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
// "4.5.6-foobar-x.3") can override all of this: it ANCHORS the version to the
// tag's core (raised by any explicit "+semver:"/feature bump on commits after
// the tag), using the tag's label and continuing its counter. The anchor can
// pull the core down as well as up, but never below the release boundary the
// branch builds on; a tag whose anchor is below that boundary is ignored.
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
	} else if mcore, ok, mvpre, err := c.mergeBoundary(mb); err != nil {
		return core{}, "", 0, false, err
	} else if ok {
		// The fork point is a release merge on the mainline. Its resulting core
		// is already established (registered at its second parent), so the branch
		// builds straight on that core: re-scanning the section here would
		// wrongly re-apply the merge's own bump on top of the core it produced.
		belowCore = mcore
		baseVPrefix = mvpre
		c.logf("other: fork point is a release merge (%s); %s section bump = none", mcore, integration)
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
	// fork point so the count matches `git rev-list mb..head`. One walk yields
	// the count, the bump, and any reference tag among the branch's commits.
	mbSet := ancestorHashesIn(mb.Hash, pool)
	rs, err := c.scanRange(head.Hash, mbSet, pool)
	if err != nil {
		return core{}, "", 0, false, err
	}
	n := rs.count
	c.logf("other: branch's own commits bump = %s", rs.bump)
	eff := max(sectionBump, rs.bump)
	if isFeatureBranch(branch) {
		eff = max(eff, bumpMinor) // feature increment takes precedence
		c.logf("other: feature branch -> effective bump forced to at least minor")
	}

	out := belowCore.bump(eff)
	c.logf("other: effective bump = %s; %d commit(s) since fork point; core %s -> %s", eff, n, belowCore, out)

	// A prerelease reference tag among the branch's own commits (mb..head)
	// anchors the version to the tag's core, continuing the tag's label and
	// counter. The anchor is raised only by EXPLICIT bumps on commits after the
	// tag; it can pull the core DOWN as well as up, but never below belowCore —
	// the release boundary the branch builds on. When the tag's anchor is below
	// that boundary, the tag is ignored and the normally-computed core stands.
	if rs.hasRef {
		after, aerr := c.scanRange(head.Hash, ancestorHashesIn(rs.nearestHash, pool), pool)
		if aerr != nil {
			return core{}, "", 0, false, aerr
		}
		lift, lerr := c.anchorLiftBump(head.Hash, rs.nearestHash, pool)
		if lerr != nil {
			return core{}, "", 0, false, lerr
		}
		anchor := rs.nearestRef.core.bump(lift)
		// The anchor CAPS the branch (can lower it below its own feature-minor)
		// only when the tag is on the branch's OWN first-parent line. A tag that
		// arrived via a merge (on a merged-in branch's side) cannot cap the
		// receiving branch, whose own weight stands; there the anchor is a
		// raise-only floor.
		onOwnLine := c.onFirstParentLine(head.Hash, rs.nearestHash, mb.Hash, pool)
		if !onOwnLine {
			if less(out, anchor) {
				c.logf("other: merged-in reference tag on %s raises core to %s", short(rs.nearestHash), anchor)
				return anchor, rs.nearestRef.label, rs.nearestRef.counter + after.count, baseVPrefix, nil
			}
			c.logf("other: merged-in reference tag on %s (%s) below computed %s; branch weight stands", short(rs.nearestHash), anchor, out)
			return out, "", n, baseVPrefix, nil
		}
		if !less(rs.nearestRef.core, belowCore) {
			counter := rs.nearestRef.counter + after.count
			c.logf("other: reference tag on %s (%s-%s.%d) anchors core to %s; counter = %d + %d after = %d",
				short(rs.nearestHash), rs.nearestRef.core, rs.nearestRef.label, rs.nearestRef.counter, anchor, rs.nearestRef.counter, after.count, counter)
			return anchor, rs.nearestRef.label, counter, baseVPrefix, nil
		}
		c.logf("other: reference tag on %s (core %s, anchor %s) is below base boundary %s; ignored",
			short(rs.nearestHash), rs.nearestRef.core, anchor, belowCore)
	}

	return out, "", n, baseVPrefix, nil
}

// onFirstParentLine reports whether target lies on the first-parent chain walked
// from head down to (but not past) stop — i.e. target is on "this branch's own"
// mainline rather than on a side branch that was merged in. The walk follows
// only Parent(0) edges via the pool, so a commit reachable only through a merge's
// second parent is not on the line.
func (c *calculator) onFirstParentLine(head, target, stop plumbing.Hash, pool map[plumbing.Hash][]plumbing.Hash) bool {
	h := head
	for {
		if h == target {
			return true
		}
		if h == stop {
			return false
		}
		parents, ok := pool[h]
		if !ok || len(parents) == 0 {
			return false
		}
		h = parents[0]
	}
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
