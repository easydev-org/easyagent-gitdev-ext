package ghclient

import (
	"context"
	"fmt"
	"sort"
	"strings"

	gogithub "github.com/google/go-github/v93/github"
)

// Account-level operations: which organizations and repositories the caller can see, and what
// its credential is actually allowed to do.

// Identity is who the token belongs to.
type Identity struct {
	Login string
	Name  string
	Email string
	Type  string
}

// Scopes describes what a token may do.
//
// TWO credential models answer differently, and conflating them is misleading:
//
//   - classic PAT: scopes live in the `x-oauth-scopes` RESPONSE HEADER, which go-github exposes
//     as Response.OAuthScopes;
//   - fine-grained PAT (or a GitHub App token): no scopes at all — permissions are
//     per-repository. An empty list here means "not a classic token", NOT "no permissions".
type Scopes struct {
	Classic []string
	// FineGrained is true when GitHub reported no classic scopes, which is how a fine-grained
	// or app token presents.
	FineGrained bool
	ExpiresAt   string
}

// IdentityAndScopes returns the caller's identity and what its token may do.
//
// One request: the scopes arrive in the header of the same call.
func (c *Client) IdentityAndScopes(ctx context.Context) (Identity, Scopes, error) {
	if c.Token == "" {
		return Identity{}, Scopes{}, fmt.Errorf("this needs a token: identity and scopes are not available anonymously")
	}
	u, resp, err := c.gh.Users.Get(ctx, "")
	if err != nil {
		return Identity{}, Scopes{}, wrap(err)
	}
	id := Identity{Login: u.GetLogin(), Name: u.GetName(), Email: u.GetEmail(), Type: u.GetType()}

	sc := Scopes{}
	// Read the scopes header OURSELVES: go-github v93 has no Response.OAuthScopes accessor, and
	// the header is the only place a classic token's scopes exist. Response embeds
	// *http.Response, so the raw header is reachable here.
	hdr := ""
	if resp != nil && resp.Response != nil {
		hdr = resp.Header.Get("x-oauth-scopes")
		sc.ExpiresAt = resp.Header.Get("github-authentication-token-expiration")
	}
	if strings.TrimSpace(hdr) != "" {
		for _, s := range strings.Split(hdr, ",") {
			if s = strings.TrimSpace(s); s != "" {
				sc.Classic = append(sc.Classic, s)
			}
		}
		sort.Strings(sc.Classic)
	} else {
		// No scopes header: a fine-grained or app token, whose permissions are per-repository.
		sc.FineGrained = true
	}
	return id, sc, nil
}

// Org is one organization the caller can see.
type Org struct {
	Login       string
	Description string
}

// Orgs lists the organizations the caller belongs to. Paged.
func (c *Client) Orgs(ctx context.Context) ([]Org, error) {
	var out []Org
	err := paginate(func(page int) (*gogithub.Response, error) {
		orgs, resp, err := c.gh.Organizations.List(ctx, "",
			&gogithub.ListOptions{Page: page, PerPage: 100})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, o := range orgs {
			out = append(out, Org{Login: o.GetLogin(), Description: o.GetDescription()})
		}
		return resp, nil
	})
	return out, err
}

// RepoRef is one repository in a listing.
type RepoRef struct {
	FullName      string
	Private       bool
	DefaultBranch string
	Archived      bool
	Description   string
	Fork          bool
}

func repoRefFromG(r *gogithub.Repository) RepoRef {
	return RepoRef{
		FullName: r.GetFullName(), Private: r.GetPrivate(),
		DefaultBranch: r.GetDefaultBranch(), Archived: r.GetArchived(),
		Description: r.GetDescription(), Fork: r.GetFork(),
	}
}

// ListOrgRepos lists an organization's repositories. Paged.
//
// `kind` is all | public | private | forks | sources | member.
func (c *Client) ListOrgRepos(ctx context.Context, org, kind string) ([]RepoRef, error) {
	if org == "" {
		return nil, fmt.Errorf("org is required")
	}
	if kind == "" {
		kind = "all"
	}
	var out []RepoRef
	err := paginate(func(page int) (*gogithub.Response, error) {
		repos, resp, err := c.gh.Repositories.ListByOrg(ctx, org, &gogithub.RepositoryListByOrgOptions{
			Type: kind, ListOptions: gogithub.ListOptions{Page: page, PerPage: 100},
		})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, r := range repos {
			out = append(out, repoRefFromG(r))
		}
		return resp, nil
	})
	return out, err
}

// ListUserRepos lists the caller's own + collaborating + organization repositories. Paged.
func (c *Client) ListUserRepos(ctx context.Context, visibility string) ([]RepoRef, error) {
	if visibility == "" {
		visibility = "all"
	}
	var out []RepoRef
	err := paginate(func(page int) (*gogithub.Response, error) {
		repos, resp, err := c.gh.Repositories.ListByAuthenticatedUser(ctx, &gogithub.RepositoryListByAuthenticatedUserOptions{
			Affiliation: "owner,collaborator,organization_member",
			Visibility:  visibility,
			ListOptions: gogithub.ListOptions{Page: page, PerPage: 100},
		})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, r := range repos {
			out = append(out, repoRefFromG(r))
		}
		return resp, nil
	})
	return out, err
}

// CreateRepoOptions describes a repository to create.
type CreateRepoOptions struct {
	// Org creates under an organization; empty creates under the authenticated user.
	Org         string
	Name        string
	Description string
	Private     bool
	// AutoInit creates an initial commit, which is the difference between a repository that can
	// be pushed to immediately and one that needs a first push to exist at all.
	AutoInit          bool
	GitignoreTemplate string
	LicenseTemplate   string
}

// CreateRepo creates a repository.
func (c *Client) CreateRepo(ctx context.Context, o CreateRepoOptions) (RepoRef, error) {
	if strings.TrimSpace(o.Name) == "" {
		return RepoRef{}, fmt.Errorf("name is required")
	}
	in := gogithub.CreateRepositoryRequest{
		Name:     o.Name,
		Private:  gogithub.Bool(o.Private),
		AutoInit: gogithub.Bool(o.AutoInit),
	}
	if o.Description != "" {
		in.Description = gogithub.String(o.Description)
	}
	if o.GitignoreTemplate != "" {
		in.GitignoreTemplate = gogithub.String(o.GitignoreTemplate)
	}
	if o.LicenseTemplate != "" {
		in.LicenseTemplate = gogithub.String(o.LicenseTemplate)
	}
	r, resp, err := c.gh.Repositories.Create(ctx, o.Org, in)
	c.note(resp)
	if err != nil {
		return RepoRef{}, wrap(err)
	}
	return repoRefFromG(r), nil
}

// RenameRepo changes a repository's name.
//
// GitHub 301s the old name to the new one, so a caller that keeps using the old slug still
// resolves — but tooling that caches it should refresh, which is why the new one is returned.
func (c *Client) RenameRepo(ctx context.Context, newName string) (RepoRef, error) {
	if strings.TrimSpace(newName) == "" {
		return RepoRef{}, fmt.Errorf("new name is required")
	}
	r, resp, err := c.gh.Repositories.Edit(ctx, c.Owner, c.Repo, &gogithub.Repository{
		Name: gogithub.String(newName),
	})
	c.note(resp)
	if err != nil {
		return RepoRef{}, wrap(err)
	}
	return repoRefFromG(r), nil
}

// DeleteRepo deletes a repository. Irreversible.
func (c *Client) DeleteRepo(ctx context.Context) error {
	if c.Token == "" {
		return fmt.Errorf("deleting a repository needs a token")
	}
	resp, err := c.gh.Repositories.Delete(ctx, c.Owner, c.Repo)
	c.note(resp)
	if err == nil {
		return nil
	}
	// A 404 MEANS IT IS ALREADY GONE, which is the state the caller asked for. Reporting it as a failure
	// makes a successful delete look broken — and it did: the repository was removed and the tool
	// answered 404, so a CI step failed over a deletion that had happened.
	//
	// IT IS NOT ALWAYS THIS SIMPLE: GitHub answers 403 for both "no permission" and "not found" on
	// DELETE, which is why CheckTokenCanDelete exists and runs first. By the time this call is made,
	// the token is known to carry `delete_repo`, so a 404 here is genuinely "nothing left to delete".
	if IsNotFound(err) {
		return nil
	}
	return wrap(err)
}

// CheckTokenCanDelete reports whether the token carries the scope deletion requires.
//
// Checked BEFORE the call: `delete_repo` is a scope most tokens deliberately omit, and GitHub
// answers 403 for both "no permission" and "not found" on DELETE — which reads as "already
// gone".
func CheckTokenCanDelete(sc Scopes) error {
	if sc.FineGrained {
		// Fine-grained tokens express this per repository, not as a scope; there is no local
		// answer, so let the API decide.
		return nil
	}
	for _, s := range sc.Classic {
		if s == "delete_repo" {
			return nil
		}
	}
	return fmt.Errorf("this token has no delete_repo scope; deleting needs a classic PAT with it")
}
