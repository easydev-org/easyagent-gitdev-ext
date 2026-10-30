package forge

import (
	"context"
	"fmt"
	"strings"
)

/**
 * Establish creates a mirror of an upstream repository, on demand.
 *
 * THE MISSING STEP. Everything else here assumes the mirror exists: Files reads it, patches commit
 * to it, pushes come out of it. Nothing created it, so the panel could list eighty GitHub
 * repositories and not one of them could be opened.
 *
 * TWO CALLS THAT MUST HAPPEN IN ORDER, and the reason is a platform limit rather than a preference:
 *
 *  1. CREATE it WITH an initial commit. A repository created empty answers 200 and then refuses
 *     every write with an empty 500 — so the second call has nowhere to put anything.
 *  2. PUSH the upstream's branch onto it. `CreateBranchFromUpstream` fetches the upstream into
 *     memory and pushes it here, which is the same path `git-sync-upstream` uses to follow it later.
 *
 * `owner` IS THE UPSTREAM'S OWNER, and the mirror keeps the upstream's name in full. It used to be
 * created under whichever account held the token, on the reasoning that only that account could write
 * — but membership, not ownership, is what permits a write, and a mirror whose name differs from its
 * upstream is one that every cross-repository page reads wrong. An empty owner means "the token's own
 * account", which is the case for a personal repository.
 */
func (c *Client) Establish(ctx context.Context, owner, name, upstreamURL, upstreamBranch, upstreamToken string) (branch string, commits string, err error) {
	name = strings.TrimSpace(name)
	owner = strings.TrimSpace(owner)
	if name == "" {
		return "", "", fmt.Errorf("name is required")
	}
	if strings.TrimSpace(upstreamURL) == "" {
		return "", "", fmt.Errorf("upstreamURL is required")
	}
	if upstreamBranch == "" {
		// ASK THE UPSTREAM WHAT ITS DEFAULT IS, rather than assuming `main`. Half the repositories in
		// the world still use `master`, and a mirror seeded from a branch that does not exist fails
		// with "the upstream has no branch main" — which names the symptom and not the assumption.
		def, derr := upstreamDefaultBranch(ctx, upstreamURL, upstreamToken)
		if derr != nil {
			return "", "", derr
		}
		upstreamBranch = def
	}

	// WHERE THE UPSTREAM IS, NOT WHERE THE TOKEN IS. A mirror of `easydev-org/thing` is called
	// `easydev-org/thing` here too, and that is load-bearing rather than tidy: the panel's issue, pull
	// request and CI pages read GitHub with the mirror's repository name, so a mirror parked under the
	// token's own account is a set of pages that 404 for a repository that plainly exists.
	//
	// The organisation is created first when it is missing — the forge answers 404 for an organisation
	// it does not have rather than making one implicitly. Creating it makes the token's account its
	// owner, which is what lets the next call write into it.
	if owner != "" {
		if _, oerr := c.EnsureOrg(ctx, owner, owner, false); oerr != nil {
			return "", "", fmt.Errorf("make sure the organisation %q exists: %w", owner, oerr)
		}
	}
	// THE PLACEHOLDER'S BRANCH IS NAMED AFTER THE UPSTREAM'S. Left to the forge it is always `main`,
	// so mirroring a `master` repository produced two branches — a `main` holding one README and the
	// real `master` — and the default pointed at the placeholder.
	if _, cerr := c.CreateRepoIn(ctx, owner, name, upstreamBranch, false); cerr != nil {
		return "", "", fmt.Errorf("create %s/%s: %w", owner, name, cerr)
	}

	// POINT THE CLIENT AT WHAT IT JUST CREATED. It was built with `NewForHost`, which has no owner or
	// repository, and every call after this one names a path that resolves against those. The owner is
	// the one asked for; only an EMPTY owner means "the token's own account", and only then is it asked
	// for.
	c.Repo = name
	c.Owner = owner
	if c.Owner == "" {
		who, werr := c.WhoAmI(ctx)
		if werr != nil {
			return "", "", fmt.Errorf("find the account that owns the new repository: %w", werr)
		}
		c.Owner = who
	}

	// The content, from the upstream, under the SAME branch name: a mirror whose main is called
	// something else would break every later step that assumes the upstream's names.
	//
	// FORCED, and only here. Creating the repository (above) necessarily writes an initial commit, so
	// its `main` already exists — and the upstream's `main` shares no ancestor with it, which makes the
	// push a non-fast-forward and gets it refused. This is the one call where replacing that branch is
	// the intent rather than a collision: the initial commit exists only because the platform would
	// not let us create an empty repository, and its content (a placeholder README) is not wanted.
	sha, perr := c.ReplaceBranchFromUpstream(ctx, upstreamBranch, upstreamURL, upstreamBranch, upstreamToken)
	if perr != nil {
		return "", "", fmt.Errorf("copy %s from the upstream: %w", upstreamBranch, perr)
	}
	return upstreamBranch, sha, nil
}
