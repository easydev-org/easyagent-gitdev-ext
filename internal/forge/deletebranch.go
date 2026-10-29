package forge

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/storage/memory"
)

/**
 * DeleteUpstreamBranch removes a branch from a remote — usually the UPSTREAM, not this forge.
 *
 * A PACKAGE FUNCTION, NOT A METHOD, and that is the point: nothing here uses the forge's API. It is a
 * git transport operation, and a method on Client would have inherited the client's construction —
 * which for the upstream means asking a Gitea server for its version at a host that is not one. That
 * is exactly what happened: GitHub answered /api/v1/version with 410 and the delete never started.
 *
 * A DIFFERENT NAME FROM DeleteBranch, which removes a branch on THIS forge through its API. Different
 * transport, different credential, different client.
 *
 * WHY THIS IS A PUSH AND NOT AN API CALL: the two hosts differ. Forgejo can delete a branch through
 * its API; GitHub's git endpoint accepts a push of "nothing" to the ref, and that is the one
 * operation both understand. Doing it by push means one code path and no per-host branching.
 *
 * THE SHAPE IS THE POINT: an empty source refspec with `+` (force) and an empty destination is the
 * protocol's way of saying "delete this ref". A non-empty source would be an update.
 *
 * It refuses the two refs a caller almost never means to delete, because both are irreversible in the
 * way that matters: the default branch removes what everybody clones, and a tag is a release marker
 * that may be referenced from outside.
 */
func DeleteUpstreamBranch(ctx context.Context, remoteURL, branch, token string) error {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return fmt.Errorf("branch is required: name the branch to delete")
	}
	if strings.TrimSpace(remoteURL) == "" {
		return fmt.Errorf("remoteURL is required: the repository the branch lives in")
	}
	// A GUARD, not a permission: deleting `main` on an upstream is usually a mistake, and the mistake
	// is not one a caller can undo from here.
	switch branch {
	case "main", "master":
		return fmt.Errorf("refusing to delete %q: it is the branch everybody clones, and deleting it "+
			"removes the repository's default from every clone", branch)
	}

	// In-memory, like every other transfer here: nothing is checked out and no objects are kept.
	store := memory.NewStorage()
	rem := git.NewRemote(store, &config.RemoteConfig{
		Name: "origin",
		URLs: []string{remoteURL},
	})

	// verifies the branch is there before asking anybody to delete it — and gives a message that names
	// the branch rather than the protocol.
	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: authFor(remoteURL, token)})
	if err != nil {
		return fmt.Errorf("list %s: %w", remoteURL, err)
	}
	want := "refs/heads/" + branch
	found := false
	for _, r := range refs {
		if r.Name().String() == want {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("the remote has no branch %q", branch)
	}

	err = rem.PushContext(ctx, &git.PushOptions{
		// AN EMPTY SOURCE IS THE DELETE. `+` allows it; without the force marker the remote refuses.
		RefSpecs: []config.RefSpec{config.RefSpec(":" + want)},
		Auth:     authFor(remoteURL, token),
	})
	if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
		return fmt.Errorf("delete %s: %w", branch, err)
	}
	return nil
}
