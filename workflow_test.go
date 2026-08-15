package main

import (
	"testing"
)

// TestWorkflowWithCrossMerges builds a repository through the full workflow
// (releases, feature/bugfix branches, cross-merges) and checks the version on
// several branches. The cross-merges (main merged back into develop) stress the
// reachability walk, which must exclude already-released commits reached via a
// path that bypasses the current section's boundary commit.
func TestWorkflowWithCrossMerges(t *testing.T) {
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
	h.want("0.2.0-ABC-1.1")

	h.checkout("develop")
	h.want("0.2.0-alpha.6")

	h.checkout("main")
	h.want("0.1.2")
}
