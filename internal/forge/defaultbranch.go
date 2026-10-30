package forge

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/storage/memory"
)

/**
 * upstreamDefaultBranch asks a remote which branch it considers its default.
 *
 * THE GIT PROTOCOL ALREADY CARRIES THE ANSWER: `HEAD` appears in the advertised refs and is a symbolic
 * ref pointing at it. Reading that is one round trip and needs no host-specific API, so a GitHub, a
 * Forgejo or a bare server all answer the same way.
 *
 * WHY IT MATTERS: `main` is not universal, and assuming it meant that mirroring a repository whose
 * default is `master` failed with "the upstream has no branch main" — an accurate message about the
 * wrong thing, since nobody had said which branch to use.
 */
func upstreamDefaultBranch(ctx context.Context, remoteURL, token string) (string, error) {
	rem := git.NewRemote(memory.NewStorage(), &config.RemoteConfig{
		Name: "origin",
		URLs: []string{remoteURL},
	})
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: authFor(remoteURL, token)})
	if err != nil {
		return "", fmt.Errorf("read %s: %w", remoteURL, err)
	}
	// HEAD's target is the default. go-git resolves a symbolic ref into a hash and keeps the name it
	// pointed at, which is what is wanted here.
	for _, r := range refs {
		if r.Name() == "HEAD" && r.Type() == plumbing.SymbolicReference {
			name := strings.TrimPrefix(r.Target().String(), "refs/heads/")
			if name != "" {
				return name, nil
			}
		}
	}
	// An EMPTY repository advertises no HEAD target. Falling back to `main` keeps the error the caller
	// gets a specific one — "the upstream has no branch main" — rather than a confusing report about a
	// missing default.
	for _, r := range refs {
		if r.Name() == "HEAD" && !r.Hash().IsZero() {
			for _, b := range refs {
				if b.Hash() == r.Hash() && b.Name().IsBranch() {
					return strings.TrimPrefix(b.Name().String(), "refs/heads/"), nil
				}
			}
		}
	}
	return "", fmt.Errorf("cannot tell which branch %s considers its default; pass one explicitly", remoteURL)
}
