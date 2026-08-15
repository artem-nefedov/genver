package main

import (
	"testing"
)

// TestCommitGraphParity builds a repository through the full workflow (releases,
// feature/bugfix branches, cross-merges), writes a real commit-graph over its
// history, and asserts that the version computed via the commit-graph fast path
// matches the version computed via the object-store path at every interesting
// point. This guards the invariant that the commit-graph is purely an
// optimization: it must never change the answer.
func TestCommitGraphParity(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.release("0.1.1")
	h.backMerge() // cross-merge to stress reachability
	h.commit("d2")
	h.release("0.1.2")
	h.backMerge()
	h.commit("d3")

	// Feature branch (minor bump), then merge it back.
	h.newBranch("feature/cool-abc")
	h.commit("f1")
	h.commit("f2")
	h.checkout("develop")
	h.merge("feature/cool-abc")
	h.deleteBranch("feature/cool-abc")

	// Bugfix branch (inherits the section's minor after the feature merge).
	h.newBranch("bugfix/ABC-1")
	h.commit("b1")

	// Write a commit-graph covering ALL commits, then check parity on several
	// branches. The commit-graph is complete here, so the fast path is fully
	// exercised.
	writeCommitGraph(t, h.g, allCommitHashes(t, h.g))

	// bugfix branch (current HEAD)
	h.assertGraphParity()
	if got := versionThroughGraph(t, h.g, true); got != "0.2.0-ABC-1.1" {
		t.Fatalf("bugfix branch version = %q, want 0.2.0-ABC-1.1", got)
	}

	h.checkout("develop")
	h.assertGraphParity()

	h.checkout("main")
	h.assertGraphParity()
}

// TestCommitGraphParityStale covers the case where the commit-graph is stale:
// commits are added AFTER the graph is written, so the graph covers only part of
// history. go-git falls back to the object store for the uncovered commits, and
// the answer must still match the no-graph path.
func TestCommitGraphParityStale(t *testing.T) {
	t.Parallel()
	h := newHarness(t)

	h.commit("root")
	h.newBranch("develop")
	h.commit("d1")
	h.release("0.1.1")
	h.commit("d2")

	// Write a commit-graph now (covers history up to d2's release only)...
	writeCommitGraph(t, h.g, allCommitHashes(t, h.g))

	// ...then keep working. These commits are NOT in the graph.
	h.commit("d3")
	h.newBranch("feature/late")
	h.commit("f-late")

	// Parity must hold on the feature branch whose commits post-date the graph.
	h.assertGraphParity()

	h.checkout("develop")
	h.assertGraphParity()
	if got := versionThroughGraph(t, h.g, true); got != "0.1.2-alpha.2" {
		t.Fatalf("develop version with stale graph = %q, want 0.1.2-alpha.2", got)
	}
}
