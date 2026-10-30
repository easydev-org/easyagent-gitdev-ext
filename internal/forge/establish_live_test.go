package forge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Establishing a mirror copies the upstream's NAME, owner included.
//
// THAT IS THE WHOLE POINT of this test: a mirror created under the token's own account instead of the
// upstream's owner was what made the panel's issue, pull request and CI pages answer 404 for a
// repository that plainly existed. The assertion is on the OWNER, because getting the repository made
// is not the thing that was wrong.
//
// FORGE_LIVE_URL=http://forge:3000 FORGE_LIVE_TOKEN=… GH_TOKEN=… GH_E2E_REPO=owner/name \
//   go test ./internal/forge -run TestLiveEstablishKeepsUpstreamOwner
func TestLiveEstablishKeepsUpstreamOwner(t *testing.T) {
	base := os.Getenv("FORGE_LIVE_URL")
	token := os.Getenv("FORGE_LIVE_TOKEN")
	repo := os.Getenv("GH_E2E_REPO")
	upTok := os.Getenv("GH_TOKEN")
	if base == "" || token == "" || repo == "" {
		t.Skip("set FORGE_LIVE_URL, FORGE_LIVE_TOKEN and GH_E2E_REPO")
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatalf("GH_E2E_REPO must be owner/name, got %q", repo)
	}

	c, err := NewForHost(base, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	branch, commit, err := c.Establish(ctx, owner, name,
		"https://github.com/"+repo+".git", "main", upTok)
	if err != nil {
		t.Fatalf("establish: %v", err)
	}
	// THE MIRROR KEEPS THE UPSTREAM'S NAME, so the name to look up is the one that was asked for — the
	// assertion below is that it really is there, under that owner.
	mirroredAs := owner + "/" + name
	if branch != "main" {
		t.Errorf("branch = %q, want main", branch)
	}
	if commit == "" {
		t.Error("no commit; the upstream was not copied")
	}

	// AND IT IS REALLY THERE, under that owner: the returned string is the client's own bookkeeping and
	// could be right while the forge disagrees.
	var got struct {
		FullName string `json:"full_name"`
		Empty    bool   `json:"empty"`
	}
	if err := c.do(ctx, "GET", "/repos/"+mirroredAs, nil, &got); err != nil {
		t.Fatalf("read back %s: %v", mirroredAs, err)
	}
	if got.Empty {
		t.Errorf("%s exists but has no commits", mirroredAs)
	}

	// CLEANED UP THROUGH THE API, because this test made it and nothing else will.
	if err := c.do(ctx, "DELETE", "/repos/"+mirroredAs, nil, nil); err != nil {
		t.Logf("cleanup: delete %s: %v", mirroredAs, err)
	}
}

// The organisation is created when the upstream's owner has no counterpart here.
func TestLiveEnsureOrg(t *testing.T) {
	base := os.Getenv("FORGE_LIVE_URL")
	token := os.Getenv("FORGE_LIVE_TOKEN")
	if base == "" || token == "" {
		t.Skip("set FORGE_LIVE_URL and FORGE_LIVE_TOKEN")
	}
	c, err := NewForHost(base, token)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := "e2e-owner-probe"
	created, err := c.EnsureOrg(ctx, name, name, false)
	if err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if !created {
		t.Fatalf("%s already exists; this test needs a name nobody has used", name)
	}
	// IDEMPOTENT: the second call finds it and says so rather than failing.
	created, err = c.EnsureOrg(ctx, name, name, false)
	if err != nil || created {
		t.Errorf("second EnsureOrg: created=%v err=%v; want created=false, err=nil", created, err)
	}
	if err := c.do(ctx, "DELETE", "/orgs/"+name, nil, nil); err != nil {
		t.Logf("cleanup: delete org %s: %v", name, err)
	}
}
