package forge

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

/**
 * CreateRepo makes a repository on the forge, with an initial commit.
 *
 * INITIALISED, ALWAYS, and that is not a detail. A repository created without a commit is REAL — it
 * answers 200 and is writable in principle — but the contents API refuses it with an empty 500, as
 * does every tree and branch call. Every later step of establishing a mirror starts by writing to
 * it, so an empty repository is a dead end that looks alive.
 *
 * IDEMPOTENT: a repository that already exists is reported as such rather than as a failure, because
 * the caller's intent is "make sure this exists" and a name collision is the answer they wanted.
 */
func (c *Client) CreateRepo(ctx context.Context, name, defaultBranch string, private bool) (existed bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("name is required")
	}
	err = c.do(ctx, http.MethodPost, "/user/repos", map[string]any{
		"name":      name,
		"private":   private,
		"auto_init": true,
		// THE BRANCH THE PLACEHOLDER COMMIT LANDS ON, named to match the upstream. Left to the forge,
		// it is always `main` — so mirroring a `master` repository produced TWO branches, a `main`
		// holding one README and the real `master`, and the default pointed at the placeholder.
		"default_branch": defaultBranch,
	}, nil)
	return existedFrom(err)
}

/**
 * CreateRepoIn makes a repository under an OWNER — an organisation, or the account itself when owner
 * is empty.
 *
 * WHY OWNERSHIP MATTERS: a mirror should be where its upstream is. GitHub's `easydev-org/thing` and the
 * mirror's `easydev-org/thing` are the same name, and that is not cosmetic — the panel's issue, pull
 * request and CI pages read GITHUB with the mirror's repository name, so a mirror parked under a
 * different owner is a page that answers 404 for a repository that plainly exists.
 *
 * The organisation must exist first: the forge answers 404 "GetOrgByName" for a name it does not have,
 * rather than creating one implicitly. EnsureOrg does that, and is separate because creating an
 * organisation is a different decision from creating a repository inside one.
 */
func (c *Client) CreateRepoIn(ctx context.Context, owner, name, defaultBranch string, private bool) (existed bool, err error) {
	owner = strings.TrimSpace(owner)
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("name is required")
	}
	if owner == "" {
		return c.CreateRepo(ctx, name, defaultBranch, private)
	}
	err = c.do(ctx, http.MethodPost, "/orgs/"+esc(owner)+"/repos", map[string]any{
		"name":           name,
		"private":        private,
		"auto_init":      true,
		"default_branch": defaultBranch,
	}, nil)
	return existedFrom(err)
}

/**
 * EnsureOrg makes an organisation exist, creating it if it does not.
 *
 * A NEW ORGANISATION HAS THE TOKEN'S ACCOUNT AS ITS OWNER, which is what makes a mirror possible at
 * all when the upstream's organisation has no counterpart here: the account that creates it can write
 * to it. When the account is already a member of an existing organisation — the usual case in a
 * deployment that mirrors its own organisation — nothing is created and nothing changes.
 *
 * IT DOES NOT CHECK MEMBERSHIP. Discovering it after a failed write would be later than useful; the
 * write itself reports a permission problem, and that message names the organisation.
 */
func (c *Client) EnsureOrg(ctx context.Context, name, display string, private bool) (created bool, err error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return false, fmt.Errorf("organisation name is required")
	}
	if err := c.do(ctx, http.MethodGet, "/orgs/"+esc(name), nil, nil); err == nil {
		return false, nil
	} else if !strings.Contains(err.Error(), "404") && !strings.Contains(err.Error(), "not found") {
		// A permission or transport failure is NOT "it does not exist", and creating it would then fail
		// with a second, less useful message.
		return false, err
	}
	if display == "" {
		display = name
	}
	if cerr := c.do(ctx, http.MethodPost, "/orgs", map[string]any{
		"username": name, "full_name": display, "visibility": visibility(private),
	}, nil); cerr != nil {
		// 422 means somebody else made it between the check and now, which is the state we wanted.
		if strings.Contains(cerr.Error(), "422") {
			return false, nil
		}
		return false, cerr
	}
	return true, nil
}

// visibility maps this extension's boolean onto the forge's enum. Private stays private; anything
// else is public, because a mirror of a public upstream that lands private cannot be read by the
// people who could read the original.
func visibility(private bool) string {
	if private {
		return "private"
	}
	return "public"
}

// existedFrom turns a create error into the (already-there, error) pair every caller wants.
//
// A name that is taken answers 409, which is what a caller who said "make sure this exists" asked for:
// reported as success-with-a-fact rather than as a failure.
func existedFrom(err error) (bool, error) {
	if err == nil {
		return false, nil
	}
	if strings.Contains(err.Error(), "409") {
		return true, nil
	}
	return false, err
}
