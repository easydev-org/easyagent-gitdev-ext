package forge

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Rebuild the mirrors a deployment needs, from a list on the command line.
//
// A TEST RATHER THAN A COMMAND because it uses the same code the panel uses and nothing else: if
// `Establish` cannot make a working mirror, this says so before anybody clicks a button and finds out.
//
// FORGE_LIVE_URL=… FORGE_LIVE_TOKEN=… GH_TOKEN=… \
//   GH_E2E_REBUILD="easydev-org/a,easydev-org/b" go test ./internal/forge -run TestLiveRebuildMirrors
func TestLiveRebuildMirrors(t *testing.T) {
	base := os.Getenv("FORGE_LIVE_URL")
	token := os.Getenv("FORGE_LIVE_TOKEN")
	list := os.Getenv("GH_E2E_REBUILD")
	upTok := os.Getenv("GH_TOKEN")
	if base == "" || token == "" || list == "" {
		t.Skip("set FORGE_LIVE_URL, FORGE_LIVE_TOKEN and GH_E2E_REBUILD=owner/a,owner/b")
	}

	c, err := NewForHost(base, token)
	if err != nil {
		t.Fatal(err)
	}

	for _, repo := range strings.Split(list, ",") {
		repo = strings.TrimSpace(repo)
		if repo == "" {
			continue
		}
		owner, name, ok := strings.Cut(repo, "/")
		if !ok {
			t.Errorf("%q is not owner/name", repo)
			continue
		}
		// EACH REPOSITORY GETS ITS OWN DEADLINE: one large upstream must not consume the budget for
		// the ones after it, and a failure here is reported against the repository that caused it.
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		// AN EMPTY BRANCH lets Establish ask the upstream which one is its default: half the world
		// still uses master, and this rebuild exists to work on whatever the upstream actually has.
		branch, commit, err := c.Establish(ctx, owner, name,
			"https://github.com/"+repo+".git", "", upTok)
		cancel()
		if err != nil {
			t.Errorf("%s: %v", repo, err)
			continue
		}
		t.Logf("%s -> %s at %s", repo, branch, commit[:min(12, len(commit))])
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
