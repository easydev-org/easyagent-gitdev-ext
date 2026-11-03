package forge

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

/**
 * The whole loop, end to end, against a real forge and a real upstream.
 *
 * Each step is a SEPARATE call, because that is what the agent will do — this does not invent a
 * combined operation. The point is to prove the sequence works and that every step leaves the state
 * the next one expects: a stale main would produce a branch based on the wrong commit, and a patch
 * against a missing branch would fail in a way that looks like a forge problem.
 *
 * Needs a WRITABLE repository: a pull mirror cannot accept commits, which is why the sync uses git
 * rather than the mirror API.
 */
func TestWholeLoopLive(t *testing.T) {
	base := os.Getenv("FORGE_LIVE_URL")
	tok := os.Getenv("FORGE_LIVE_TOKEN")
	repo := os.Getenv("LOOP_REPO") // e.g. agentdev/loop-test
	upstream := os.Getenv("UPSTREAM_LIVE_URL")
	upTok := os.Getenv("UPSTREAM_LIVE_TOKEN")
	if base == "" || tok == "" || repo == "" || upstream == "" {
		t.Skip("set FORGE_LIVE_*, LOOP_REPO and UPSTREAM_LIVE_* to run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	defer cancel()

	c, err := New(base, tok, base+"/"+repo+".git")
	if err != nil {
		t.Fatal(err)
	}

	// 1. Bring main up to the upstream, so the branch below starts from the current state rather than
	//    from whatever the mirror last saw.
	moved, before, after, err := c.SyncUpstream(ctx, "main", upstream, upTok)
	if err != nil {
		t.Fatalf("sync main: %v", err)
	}
	t.Logf("1. sync main   moved=%v %s -> %s", moved, shortSHA(before), shortSHA(after))

	// 2. Cut a working branch from it.
	//
	// UNIQUE PER RUN, for the same reason the file below is: the extension refuses to create a branch
	// whose name is taken, because silently handing back the old one would mean patching, pushing and
	// updating a pull request under a name the caller believed was new.
	branch := fmt.Sprintf("feat/loop-%d", time.Now().UnixNano())
	if err := c.CreateBranch(ctx, branch, "main"); err != nil && err != ErrBranchExists {
		t.Fatalf("create branch: %v", err)
	}
	t.Logf("2. branch      %s", branch)

	// 3. Change a file with a patch — one commit, whatever the file count.
	//
	// The path carries the RUN's timestamp: this test is repeatable, and `Add File` on a path that
	// already exists is REFUSED by design (an Add over an existing file is almost always a caller who
	// meant Update, and overwriting would discard content the patch never mentioned). Reusing a fixed
	// name would make the second run fail for a reason that has nothing to do with what is tested.
	path := fmt.Sprintf("LOOP-%d.md", time.Now().UnixNano())
	patch := "*** Begin Patch\n" +
		"*** Add File: " + path + "\n+written by the whole-loop probe\n" +
		"*** End Patch\n"
	plan, commit, err := c.ApplyPatch(ctx, branch, "probe: one file, one commit", patch)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	t.Logf("3. patch       %d change(s), commit %s", len(plan), shortSHA(commit))

	// 4. Push that branch to the upstream, under a name of our choosing.
	//
	// Also unique: the push is deliberately NOT forced, so re-pushing a different history onto an
	// existing upstream branch is refused — which is right for a real caller and awkward for a test
	// that runs twice.
	destBranch := fmt.Sprintf("probe/loop-%d", time.Now().UnixNano())
	sha, err := c.PushBranch(ctx, branch, upstream, destBranch, upTok)
	if err != nil {
		t.Fatalf("push branch: %v", err)
	}
	t.Logf("4. push        %s -> %s @ %s", branch, destBranch, shortSHA(sha))

	// What arrived upstream must be the commit the patch produced — not merely "a push succeeded".
	if sha != commit {
		t.Fatalf("pushed %s, but the commit made was %s", shortSHA(sha), shortSHA(commit))
	}
}
