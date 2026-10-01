package main

import (
	"fmt"
	"testing"
)

// buildAnchorLiftRepo builds a develop history that maximizes anchorLiftBump /
// integratesAnchor work: a release boundary, a reference-tagged feature branch
// (the downward anchor), then many INDEPENDENT feature merges landing on develop
// after the anchor. Each feature merge is a merge commit that the old
// integratesAnchor re-scanned with a fresh full ancestor-set allocation, so the
// cost grew as O(merges * poolSize). The shared ancestor memo removes that.
//
// mergesAfter controls how many independent feature merges land after the
// anchor; each also grows the pool, so this stresses both dimensions at once.
func buildAnchorLiftRepo(tb testing.TB, mergesAfter int) *harness {
	h := newHarnessNamed(tb, "main")
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.56.0", mg)
	h.checkout("develop")
	h.merge("main")

	// A reference-tagged feature branch merged into develop: establishes the
	// 1.2.3 downward anchor. A plain commit sits after the tag on the branch.
	h.newBranch("feature/anchored")
	h.commit("fa1")
	h.tag("1.2.3-anchored.5", mustHead(tb, h))
	h.commit("fa2")
	h.checkout("develop")
	h.merge("feature/anchored")

	// Many independent feature branches, each with a couple of commits, merged
	// into develop AFTER the anchor. Every merge commit is a descendant of the
	// anchor and a candidate that integratesAnchor inspects.
	for i := range mergesAfter {
		br := fmt.Sprintf("feature/indep-%d", i)
		h.newBranch(br)
		h.commit(fmt.Sprintf("i%d-a", i))
		h.commit(fmt.Sprintf("i%d-b", i))
		h.checkout("develop")
		h.merge(br)
	}
	return h
}

func benchmarkAnchorLift(b *testing.B, mergesAfter int) {
	h := buildAnchorLiftRepo(b, mergesAfter)
	branch, err := h.g.headBranch()
	if err != nil {
		b.Fatal(err)
	}
	head, err := h.g.headCommit()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		// A fresh calculator each iteration: mirrors a real invocation and avoids
		// cross-iteration memo reuse (the memo is per anchorLiftBump call anyway).
		h.g.fpChainCache = nil    // fresh process would not have a warm chain cache
		h.g.parentPoolCache = nil // nor a warm parent-pool cache
		calc, err := newCalculator(h.g)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := calc.Calculate(branch, head); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAnchorLiftManyMerges20(b *testing.B)  { benchmarkAnchorLift(b, 20) }
func BenchmarkAnchorLiftManyMerges50(b *testing.B)  { benchmarkAnchorLift(b, 50) }
func BenchmarkAnchorLiftManyMerges100(b *testing.B) { benchmarkAnchorLift(b, 100) }

// buildFeatureWithDevelopMergedRepo builds the topology the otherVersion counter
// and forkBase fixes target: a develop branch that has advanced through many PR
// merges, and a feature branch that has (a) its own commits and (b) merged that
// large develop back INTO itself. Computing the version on that feature branch
// exercises forkBase's first-parent walk, permanentMainlineWalls (first-parent
// chains of BOTH develop and main), and the dedicated counter scanRange over the
// full pool — the parts these changes add cost to.
//
// developMerges controls how much develop (and thus the pool and both mainlines)
// grows before it is merged into the feature branch.
func buildFeatureWithDevelopMergedRepo(tb testing.TB, developMerges int) *harness {
	h := newHarnessNamed(tb, "main")
	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.13.0", mg)
	h.checkout("develop")
	h.merge("main")

	// Feature branch off develop with a few of its own commits.
	const feat = "feature/cool-abc"
	h.newBranch(feat)
	h.commit("f1")
	h.commit("f2")
	h.commit("f3")

	// develop advances substantially: many independent short-lived branches land
	// via PR merges, growing develop's mainline (walled off by the counter) and
	// the overall pool.
	h.checkout("develop")
	for i := range developMerges {
		br := fmt.Sprintf("bugfix/indep-%d", i)
		h.newBranch(br)
		h.commit(fmt.Sprintf("i%d-a", i))
		h.commit(fmt.Sprintf("i%d-b", i))
		h.checkout("develop")
		h.mergePR(br, 100+i, "acme-org")
	}

	// Merge the large develop back INTO the feature branch, then add one more own
	// commit so HEAD is the branch's own line above the develop-merge.
	h.checkout(feat)
	h.merge("develop")
	h.commit("f4")
	return h
}

func benchmarkFeatureWithDevelopMerged(b *testing.B, developMerges int) {
	h := buildFeatureWithDevelopMergedRepo(b, developMerges)
	branch, err := h.g.headBranch()
	if err != nil {
		b.Fatal(err)
	}
	head, err := h.g.headCommit()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.g.fpChainCache = nil    // fresh process would not have a warm chain cache
		h.g.parentPoolCache = nil // nor a warm parent-pool cache
		calc, err := newCalculator(h.g)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := calc.Calculate(branch, head); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFeatureWithDevelopMerged20(b *testing.B)  { benchmarkFeatureWithDevelopMerged(b, 20) }
func BenchmarkFeatureWithDevelopMerged50(b *testing.B)  { benchmarkFeatureWithDevelopMerged(b, 50) }
func BenchmarkFeatureWithDevelopMerged100(b *testing.B) { benchmarkFeatureWithDevelopMerged(b, 100) }

// buildFeatureMergesOnMainBackMergedRepo builds the exact topology the
// released-main-merge fix targets: MANY feature branches merged DIRECTLY into
// main (so main's first-parent chain is a long run of feature-merge commits, all
// recorded in releasedMainMerges), after which main is merged back into develop.
// Computing the version on develop then walks a section that contains every one
// of those released feature merges, so it exercises both halves of the fix at
// scale:
//
//   - developBoundaries populates releasedMainMerges by scanning main's whole
//     first-parent chain — cost grows with featureMerges.
//   - scanRange -> explicitCommitBump does a releasedMainMerges lookup for each
//     of those merges in the develop section (and, in the unfixed code, would
//     instead call featureMergeBump on each). This is where a double-minor would
//     otherwise be computed, so it is the path whose behavior the fix changed.
//
// featureMerges controls how many feature merges land directly on main (and thus
// the length of main's first-parent chain and the size of develop's section).
func buildFeatureMergesOnMainBackMergedRepo(tb testing.TB, featureMerges int) *harness {
	h := newHarnessNamed(tb, "main")
	h.commit("root")

	// A develop branch exists (the version is computed on it) and is released
	// once so there is a tagged boundary below the run of direct main merges.
	h.newBranch("develop")
	h.commit("d1")
	h.checkout("main")
	mg := h.merge("develop")
	h.tag("0.1.0", mg)

	// Many feature branches cut directly from main and merged straight back into
	// main. Each merge commit lands on main's own first-parent chain and advances
	// main's release core by a minor.
	h.checkout("main")
	for i := range featureMerges {
		br := fmt.Sprintf("feature/on-main-%d", i)
		h.newBranch(br)
		h.commit(fmt.Sprintf("m%d-a", i))
		h.commit(fmt.Sprintf("m%d-b", i))
		h.checkout("main")
		h.merge(br)
	}

	// Merge the advanced main back INTO develop: every direct feature merge now
	// re-enters develop's section, where explicitCommitBump must recognize each as
	// an already-released main merge and NOT re-apply its minor.
	h.checkout("develop")
	h.merge("main")
	return h
}

func benchmarkFeatureMergesOnMainBackMerged(b *testing.B, featureMerges int) {
	h := buildFeatureMergesOnMainBackMergedRepo(b, featureMerges)
	branch, err := h.g.headBranch()
	if err != nil {
		b.Fatal(err)
	}
	head, err := h.g.headCommit()
	if err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		h.g.fpChainCache = nil    // fresh process would not have a warm chain cache
		h.g.parentPoolCache = nil // nor a warm parent-pool cache
		calc, err := newCalculator(h.g)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := calc.Calculate(branch, head); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkFeatureMergesOnMainBackMerged20(b *testing.B) {
	benchmarkFeatureMergesOnMainBackMerged(b, 20)
}
func BenchmarkFeatureMergesOnMainBackMerged50(b *testing.B) {
	benchmarkFeatureMergesOnMainBackMerged(b, 50)
}
func BenchmarkFeatureMergesOnMainBackMerged100(b *testing.B) {
	benchmarkFeatureMergesOnMainBackMerged(b, 100)
}
