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
