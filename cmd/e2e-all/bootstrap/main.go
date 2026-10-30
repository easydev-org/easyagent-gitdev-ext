// Prepare a freshly installed Forgejo for the suite: one user, one token, one repository.
//
// A FORGEJO THAT HAS JUST BOOTED HAS NO USERS AT ALL, which means it cannot be used by anything —
// not even to create the first account through the API, because the API needs an account. The
// account is made with Forgejo's own CLI inside the container, and everything after that goes
// through the API like a real deployment would.
//
// IT PRINTS WHAT THE SUITE NEEDS as KEY=VALUE, so a CI step can put it in the environment without
// this program having to know how CI works.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

func main() {
	var (
		base     = flag.String("url", envOr("E2E_FORGE_URL", "http://127.0.0.1:3000"), "forge base url")
		user     = flag.String("user", envOr("E2E_FORGE_USER", "e2e"), "account to create")
		password = flag.String("password", envOr("E2E_FORGE_PASSWORD", "e2e-password-1"), "its password")
		email    = flag.String("email", envOr("E2E_FORGE_EMAIL", "e2e@example.invalid"), "its email")
		container = flag.String("container", envOr("E2E_FORGE_CONTAINER", "ci-forge"), "docker container to run the CLI in")
		// The upstream this mirror copies, whose OWNER AND NAME the mirror takes. There is no separate
		// `-repo`: a mirror is named after the thing it mirrors, and a flag that could disagree with
		// the upstream would be a way to make the two differ.
		upstream = flag.String("upstream", envOr("GH_E2E_UPSTREAM", "https://github.com/easydev-org/test.git"), "upstream git url to seed from")
		upToken  = flag.String("upstream-token", os.Getenv("GH_TOKEN"), "credential for the upstream")
	)
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	// THE FIRST ACCOUNT CANNOT COME FROM THE API. `forgejo admin user create` is the documented way in,
	// and it has to run inside the container because that is where the CLI and the database are.
	args := []string{
		"exec", "-u", "git", *container,
		"forgejo", "admin", "user", "create",
		"--admin", "--username", *user, "--password", *password,
		"--email", *email, "--must-change-password=false",
	}
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Run(); err != nil {
		// A SECOND RUN finds the user already there, which is the answer we wanted. Anything else is a
		// real failure and its output is the only clue.
		if !strings.Contains(out.String(), "already exists") {
			fatalf("create the first account: %v\n%s", err, out.String())
		}
	}

	// FROM HERE ON IT IS THE API, exactly as a deployment would do it.
	token, err := createToken(ctx, *base, *user, *password)
	if err != nil {
		fatalf("create a token: %v", err)
	}

	// THE MIRROR IS SEEDED FROM ITS UPSTREAM, not initialised empty and patched later.
	//
	// AN auto_init REPOSITORY HAS A ROOT COMMIT OF ITS OWN, and one that shares no ancestor with the
	// upstream's — so the first real sync is refused as a non-fast-forward, and any branch cut from it
	// has "no history in common with main", which is what GitHub said when the pull request was opened.
	// forge.Establish is the code that already knows this: create with a commit, then FORCE the
	// upstream's branch over it.
	// THE BASE URL, WITHOUT /api/v1. NewForHost appends the API prefix itself (gitea.NewClient does
	// too), so passing the API base produced /api/v1/api/v1 and a 404 from a client that had been
	// given a valid server.
	fc, err := forge.NewForHost(*base, token)
	if err != nil {
		fatalf("forge client: %v", err)
	}
	// THE MIRROR KEEPS THE UPSTREAM'S FULL NAME, owner included. The owner comes from the upstream URL
	// rather than from a flag, because it is the fact that makes every cross-repository page work: the
	// panel reads GitHub with the mirror's name, so the two have to be the same name.
	upOwner, upName := ownerAndName(*upstream)
	if upOwner == "" {
		fatalf("cannot read an owner from %q", *upstream)
	}
	// Establish returns the BRANCH it copied and the commit — not a name. The name is known without
	// asking, because a mirror keeps the upstream surname. (Taking the first return value here was a bug
	// that compiled: it made the mirror identity "main", and every git-* tool then failed with
	// cannot-derive-owner/repo from "http://.../main.git".)
	if _, _, err := fc.Establish(ctx, upOwner, upName, *upstream, "main", *upToken); err != nil {
		fatalf("seed the mirror from %s: %v", *upstream, err)
	}
	mirroredAs := upOwner + "/" + upName

	// THE VALUES THE CALLER NEEDS, on stdout, one per line.
	fmt.Printf("E2E_FORGE_LOGIN=%s\n", *user)
	fmt.Printf("E2E_FORGE_TOKEN=%s\n", token)
	// THE NAME IT ACTUALLY HAS, not the one that was asked for: the mirror keeps the upstream's owner,
	// so this is `easydev-org/thing` rather than `<forge-account>/thing`.
	fmt.Printf("E2E_MIRROR_REPO=%s\n", mirroredAs)
	fmt.Printf("E2E_FORGE_URL=%s\n", strings.TrimRight(*base, "/"))
}

// createToken makes an access token through basic auth, which is the one call that accepts a password.
func createToken(ctx context.Context, base, user, password string) (string, error) {
	body, _ := json.Marshal(map[string]any{
		"name":   fmt.Sprintf("e2e-%d", time.Now().Unix()),
		"scopes": []string{"all"},
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/api/v1/users/"+user+"/tokens", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	var got struct {
		SHA1 string `json:"sha1"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return "", fmt.Errorf("status %d: %w", resp.StatusCode, err)
	}
	if got.SHA1 == "" {
		return "", fmt.Errorf("status %d: the forge returned no token", resp.StatusCode)
	}
	// A SHA1 token from this endpoint is shown ONCE and is not usable as a bearer until it is named.
	// Forgejo accepts it as `token <sha1>`; the suite uses it as-is.
	return got.SHA1, nil
}



// ownerAndName reads owner and repository from a git URL — https, ssh, or the trailing path of either.
//
// A FUNCTION RATHER THAN A REGEX AT THE CALL SITE: the same parse appears in a dozen places and they
// drift. This one handles the shapes a caller actually pastes.
func ownerAndName(raw string) (owner, name string) {
	raw = strings.TrimSpace(raw)
	raw = strings.TrimSuffix(raw, ".git")
	raw = strings.TrimRight(raw, "/")
	if i := strings.LastIndex(raw, ":"); i >= 0 && !strings.Contains(raw[i:], "//") {
		// scp-style: git@host:owner/name
		raw = raw[i+1:]
	} else if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			raw = rest[j+1:]
		}
	}
	parts := strings.Split(raw, "/")
	if len(parts) < 2 {
		return "", ""
	}
	return parts[len(parts)-2], parts[len(parts)-1]
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "bootstrap: "+f+"\n", a...)
	os.Exit(2)
}
