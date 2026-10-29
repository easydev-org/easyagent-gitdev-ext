package forge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Deleting a branch on a REAL upstream, because that is the only place the operation can be wrong:
// go-git's delete is a push of an empty refspec, and whether the server accepts it is the question.
//
// GH_E2E=1 GH_TOKEN=… GH_E2E_REPO=owner/name go test ./internal/forge -run TestLiveDeleteUpstreamBranch
func TestLiveDeleteUpstreamBranch(t *testing.T) {
	if os.Getenv("GH_E2E") != "1" {
		t.Skip("set GH_E2E=1, GH_TOKEN and GH_E2E_REPO")
	}
	token := os.Getenv("GH_TOKEN")
	repo := os.Getenv("GH_E2E_REPO")
	branch := os.Getenv("GH_E2E_BRANCH")
	if token == "" || repo == "" || branch == "" {
		t.Skip("set GH_TOKEN, GH_E2E_REPO and GH_E2E_BRANCH (a branch that exists on the upstream)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	url := "https://github.com/" + repo + ".git"
	if err := DeleteUpstreamBranch(ctx, url, branch, token); err != nil {
		t.Fatalf("delete %s: %v", branch, err)
	}

	// GONE, verified by asking again: a delete that reports success and leaves the ref is the failure
	// mode worth testing, and the only way to see it is to look.
	err := DeleteUpstreamBranch(ctx, url, branch, token)
	if err == nil || !strings.Contains(err.Error(), "no branch") {
		t.Fatalf("after delete, a second delete should report the branch is gone; got: %v", err)
	}
}

// The guard, which needs no network: the default branch is refused before anything is contacted.
func TestDeleteUpstreamBranchRefusesDefault(t *testing.T) {
	for _, branch := range []string{"main", "master"} {
		err := DeleteUpstreamBranch(context.Background(), "https://example.invalid/x.git", branch, "")
		if err == nil || !strings.Contains(err.Error(), "refusing to delete") {
			t.Errorf("%s: want a refusal, got %v", branch, err)
		}
	}
}
