package ghclient

import (
	"context"
	"strings"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/uihandler"
)

// BrowseSource reads the account-level view for the UI: repositories, organisations, releases and
// workflows.
//
// SAME SHAPE AS THE OTHER SOURCES — a token resolver and a tenant lookup, a client per request — but
// this one builds a client with NO repository, because the first question it answers is which
// repository.
type BrowseSource struct {
	TokenFor   func(tenant string) string
	TenantFrom func(ctx context.Context) (string, error)
}

func (s *BrowseSource) token(ctx context.Context) (string, error) {
	tenant := ""
	if s.TenantFrom != nil {
		t, err := s.TenantFrom(ctx)
		if err != nil {
			return "", err
		}
		tenant = t
	}
	if s.TokenFor == nil {
		return "", nil
	}
	return strings.TrimSpace(s.TokenFor(tenant)), nil
}

// account builds a client that is not tied to a repository.
//
// REMOTE IS EMPTY, and that is the whole point: `New` parses a remote into owner/repo and REFUSES
// anything that is not that shape — including "https://github.com/", which is a host and not a
// repository. An empty remote skips the parse, which is exactly right for the account endpoints:
// they take the owner from the path they are calling, not from a repository.
func (s *BrowseSource) account(ctx context.Context) (*Client, error) {
	token, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	return New(Options{Token: token})
}

func (s *BrowseSource) repo(ctx context.Context, name string) (*Client, error) {
	token, err := s.token(ctx)
	if err != nil {
		return nil, err
	}
	return New(Options{Remote: "https://github.com/" + strings.Trim(name, "/"), Token: token})
}

// Repos lists repositories; `org` narrows to one organisation.
func (s *BrowseSource) Repos(ctx context.Context, org string) ([]uihandler.RepoSummary, error) {
	// An org listing and a user listing are different API calls with different pagination, so the
	// branch is here rather than a parameter passed through.
	if strings.TrimSpace(org) != "" {
		c, err := s.account(ctx)
		if err != nil {
			return nil, err
		}
		list, err := c.ListOrgRepos(ctx, org, "all")
		if err != nil {
			return nil, err
		}
		return repoSummaries(list), nil
	}
	c, err := s.account(ctx)
	if err != nil {
		return nil, err
	}
	list, err := c.ListUserRepos(ctx, "all")
	if err != nil {
		return nil, err
	}
	return repoSummaries(list), nil
}

func repoSummaries(list []RepoRef) []uihandler.RepoSummary {
	out := make([]uihandler.RepoSummary, 0, len(list))
	for _, r := range list {
		out = append(out, uihandler.RepoSummary{
			FullName: r.FullName, Description: r.Description, Private: r.Private,
			Archived: r.Archived, Fork: r.Fork, DefaultBranch: r.DefaultBranch,
		})
	}
	return out
}

// Orgs lists the organisations the caller belongs to.
func (s *BrowseSource) Orgs(ctx context.Context) ([]uihandler.OrgSummary, error) {
	c, err := s.account(ctx)
	if err != nil {
		return nil, err
	}
	list, err := c.Orgs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uihandler.OrgSummary, 0, len(list))
	for _, o := range list {
		out = append(out, uihandler.OrgSummary{Login: o.Login, Description: o.Description})
	}
	return out, nil
}

// Whoami names the authenticated account.
func (s *BrowseSource) Whoami(ctx context.Context) (string, error) {
	c, err := s.account(ctx)
	if err != nil {
		return "", err
	}
	id, _, err := c.IdentityAndScopes(ctx)
	if err != nil {
		return "", err
	}
	// The LOGIN, not the display name: it is what a repository path is built from, and a panel that
	// showed "Ada Lovelace" where the URLs need "ada" would be a name the reader cannot type.
	return id.Login, nil
}

// Releases lists a repository's releases, newest first.
func (s *BrowseSource) Releases(ctx context.Context, repo string, limit int) ([]uihandler.ReleaseSummary, error) {
	c, err := s.repo(ctx, repo)
	if err != nil {
		return nil, err
	}
	list, err := c.Releases(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]uihandler.ReleaseSummary, 0, len(list))
	for _, r := range list {
		out = append(out, uihandler.ReleaseSummary{
			Tag: r.TagName, Name: r.Name, Draft: r.Draft, Prerelease: r.Prerelease,
			PublishedAt: r.PublishedAt, URL: r.HTMLURL, Body: r.Body, Assets: len(r.Assets),
		})
	}
	return out, nil
}

// Workflows lists a repository's workflow definitions.
func (s *BrowseSource) Workflows(ctx context.Context, repo string) ([]uihandler.WorkflowSummary, error) {
	c, err := s.repo(ctx, repo)
	if err != nil {
		return nil, err
	}
	list, err := c.ListWorkflows(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]uihandler.WorkflowSummary, 0, len(list))
	for _, w := range list {
		out = append(out, uihandler.WorkflowSummary{
			ID: w.ID, Name: w.Name, Path: w.Path, State: w.State,
		})
	}
	return out, nil
}
