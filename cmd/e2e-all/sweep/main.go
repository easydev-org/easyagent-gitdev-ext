// Delete the repositories and branches this suite leaves behind when a run FAILS.
//
// THE SUITE CLEANS UP AFTER ITSELF when it finishes — but a run that is cancelled, times out, or dies
// part way cannot, and what it created stays on GitHub. Twenty-eight `e2e-scratch-*` repositories
// accumulated that way while the suite itself was being written, on a repository whose "repositories"
// list is what the panel shows a reader.
//
// IT IS NOT PART OF THE TOOLS. There is no `gh-repo-bulk-delete` and there should not be: the reason
// to delete one repository is deliberate, and a tool that deletes many by pattern is a tool whose
// worst case is somebody else's repository. This program is run by a person who has just read the
// list, and it requires a pattern they typed.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	var (
		owner   = flag.String("owner", envOr("GH_E2E_OWNER", ""), "organisation or user")
		token   = flag.String("token", os.Getenv("GH_TOKEN"), "credential")
		pattern = flag.String("pattern", "e2e-scratch-", "name prefix to delete")
		branch  = flag.String("branch-pattern", "", "also delete branches with this prefix")
		repo    = flag.String("repo", "", "repository whose branches to sweep (with -branch-pattern)")
		dryRun  = flag.Bool("dry-run", false, "say what would be deleted, delete nothing")
	)
	flag.Parse()
	if *token == "" {
		fatalf("a token is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	deleted, failed := 0, 0

	if *owner != "" {
		// THE LIST IS PAGED, and a single page would report success after deleting the first hundred —
		// which is the failure mode that leaves exactly the invisible remainder this program exists to
		// remove.
		for page := 1; ; page++ {
			var repos []struct {
				FullName string `json:"full_name"`
				Name     string `json:"name"`
			}
			url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&page=%d&type=all", *owner, page)
			if err := doJSON(ctx, http.MethodGet, url, *token, nil, &repos); err != nil {
				fatalf("list repositories: %v", err)
			}
			if len(repos) == 0 {
				break
			}
			for _, r := range repos {
				if !strings.HasPrefix(r.Name, *pattern) {
					continue
				}
				if *dryRun {
					fmt.Println("would delete repo", r.FullName)
					deleted++
					continue
				}
				if err := doJSON(ctx, http.MethodDelete,
					"https://api.github.com/repos/"+r.FullName, *token, nil, nil); err != nil {
					fmt.Fprintf(os.Stderr, "delete %s: %v\n", r.FullName, err)
					failed++
					continue
				}
				fmt.Println("deleted repo", r.FullName)
				deleted++
			}
		}
	}

	if *branch != "" && *repo != "" {
		var branches []struct {
			Name string `json:"name"`
		}
		// One page of a hundred: a test repository that has more test branches than that has a
		// different problem.
		if err := doJSON(ctx, http.MethodGet,
			fmt.Sprintf("https://api.github.com/repos/%s/branches?per_page=100", *repo),
			*token, nil, &branches); err != nil {
			fatalf("list branches: %v", err)
		}
		for _, b := range branches {
			if !strings.HasPrefix(b.Name, *branch) {
				continue
			}
			if *dryRun {
				fmt.Println("would delete branch", b.Name)
				deleted++
				continue
			}
			if err := doJSON(ctx, http.MethodDelete,
				fmt.Sprintf("https://api.github.com/repos/%s/git/refs/heads/%s", *repo, b.Name),
				*token, nil, nil); err != nil {
				fmt.Fprintf(os.Stderr, "delete branch %s: %v\n", b.Name, err)
				failed++
				continue
			}
			fmt.Println("deleted branch", b.Name)
			deleted++
		}
	}

	fmt.Printf("%d deleted, %d failed\n", deleted, failed)
	if failed > 0 {
		os.Exit(1)
	}
}

func doJSON(ctx context.Context, method, url, token string, in, out any) error {
	var body *bytes.Reader
	if in != nil {
		raw, _ := json.Marshal(in)
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "token "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(buf.String()))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "sweep: "+f+"\n", a...)
	os.Exit(2)
}
