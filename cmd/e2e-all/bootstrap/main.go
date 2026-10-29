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
)

func main() {
	var (
		base     = flag.String("url", envOr("E2E_FORGE_URL", "http://127.0.0.1:3000"), "forge base url")
		user     = flag.String("user", envOr("E2E_FORGE_USER", "e2e"), "account to create")
		password = flag.String("password", envOr("E2E_FORGE_PASSWORD", "e2e-password-1"), "its password")
		email    = flag.String("email", envOr("E2E_FORGE_EMAIL", "e2e@example.invalid"), "its email")
		repo     = flag.String("repo", envOr("E2E_FORGE_REPO", "mirror"), "repository to create")
		container = flag.String("container", envOr("E2E_FORGE_CONTAINER", "ci-forge"), "docker container to run the CLI in")
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
	if err := createRepo(ctx, *base, token, *repo); err != nil {
		fatalf("create the mirror repository: %v", err)
	}

	// THE VALUES THE CALLER NEEDS, on stdout, one per line.
	fmt.Printf("E2E_FORGE_LOGIN=%s\n", *user)
	fmt.Printf("E2E_FORGE_TOKEN=%s\n", token)
	fmt.Printf("E2E_MIRROR_REPO=%s/%s\n", *user, *repo)
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

// createRepo makes the repository the mirror tools will write to.
func createRepo(ctx context.Context, base, token, name string) error {
	body, _ := json.Marshal(map[string]any{"name": name, "auto_init": false, "private": false})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(base, "/")+"/api/v1/user/repos", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusCreated {
		return nil
	}
	// 409: it is already there, which on a re-run is the state the suite wants.
	if resp.StatusCode == http.StatusConflict {
		return nil
	}
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return fmt.Errorf("status %d: %s", resp.StatusCode, buf.String())
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
