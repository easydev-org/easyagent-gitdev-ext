package ghclient

import (
	"context"
	"fmt"
	"strings"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/forgeconfig"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/uihandler"
)

/**
 * MirroredSource answers which repositories the MIRROR has.
 *
 * IT LIVES HERE BECAUSE THE PANEL'S OTHER SOURCES DO, not because it has anything to do with GitHub.
 * The adapter needs the mirror's configuration — which lives alongside the GitHub token in the same
 * extension — and this is where a `TokenFor`-shaped dependency is already the pattern.
 *
 * ONE FORGE CALL, THEN A SET MEMBERSHIP TEST: the alternative is one request per repository, which
 * on a page listing eighty would be eighty round trips to answer a question the forge can answer
 * once.
 */
type MirroredSource struct {
	// Cfg returns the mirror's server list for a tenant.
	Cfg func(tenant string) forgeconfig.Config
	// UpstreamToken returns the GITHUB token used to fetch the upstream. Separate from the mirror's
	// own token because they are different hosts: one writes to the forge, the other reads GitHub.
	UpstreamToken func(tenant string) string
	// TenantFrom resolves the caller for a request.
	TenantFrom func(ctx context.Context) (string, error)
}

// Mirrored reports each of `repos`' state in the mirror.
func (s *MirroredSource) Mirrored(ctx context.Context, repos []string) (map[string]uihandler.MirrorState, error) {
	tenant := ""
	if s.TenantFrom != nil {
		t, err := s.TenantFrom(ctx)
		if err != nil {
			return nil, err
		}
		tenant = t
	}
	cfg := s.Cfg(tenant)
	if len(cfg.Servers) == 0 {
		// No mirror configured: nothing is there, and saying so is the honest answer rather than an
		// error that would hide every row.
		return map[string]uihandler.MirrorState{}, nil
	}
	srv := cfg.Servers[0]
	// NewForHost, not New: this call is ABOVE a repository — it lists them — and a placeholder
	// remote would put a repository name that does not exist into every error it produced.
	c, err := forge.NewForHost(srv.APIBase(), srv.Token)
	if err != nil {
		return nil, err
	}
	have, err := c.ListRepos(ctx)
	if err != nil {
		return nil, err
	}
	// A MAP OF STATES, not a set: a repository with no commits exists and cannot be browsed, and the
	// two must not look the same on the page.
	state := make(map[string]uihandler.MirrorState, len(have))
	for _, r := range have {
		key := strings.ToLower(strings.Trim(r.FullName, "/"))
		if r.Empty {
			state[key] = uihandler.MirrorEmpty
			continue
		}
		state[key] = uihandler.MirrorReady
	}
	out := make(map[string]uihandler.MirrorState, len(repos))
	for _, r := range repos {
		if st, ok := state[strings.ToLower(strings.Trim(r, "/"))]; ok {
			out[r] = st
			continue
		}
		out[r] = uihandler.MirrorMissing
	}
	return out, nil
}

/**
 * Establish creates a mirror of one repository from its upstream.
 *
 * THE UPSTREAM IS GITHUB, and the credentials differ on each side: the forge client holds the mirror's
 * token and the caller's GitHub token goes to the fetch. Both come from this extension's configuration,
 * which is why this adapter — and not the forge package — knows how to pair them.
 *
 * The repository is created under the FORGE ACCOUNT, not under the upstream's owner: the account that
 * holds the token is the one that can write, and a mirror owned by an organisation the token cannot
 * push to would be created and then unfillable.
 */
func (s *MirroredSource) Establish(ctx context.Context, repo string) (string, string, string, error) {
	tenant := ""
	if s.TenantFrom != nil {
		t, err := s.TenantFrom(ctx)
		if err != nil {
			return "", "", "", err
		}
		tenant = t
	}
	cfg := s.Cfg(tenant)
	if len(cfg.Servers) == 0 {
		return "", "", "", fmt.Errorf("no mirror is configured, so there is nowhere to copy it to")
	}
	srv := cfg.Servers[0]
	c, err := forge.NewForHost(srv.APIBase(), srv.Token)
	if err != nil {
		return "", "", "", err
	}

	repo = strings.Trim(repo, "/")
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return "", "", "", fmt.Errorf("repo must be owner/name, got %q", repo)
	}

	// The mirror's repository NAME is the upstream's name: the panel's routes and the tools all key
	// on it, and a prefixed copy would make the mirror unrecognisable as the thing it mirrors.
	upstream := "https://github.com/" + repo + ".git"
	upTok := ""
	if s.UpstreamToken != nil {
		upTok = strings.TrimSpace(s.UpstreamToken(tenant))
	}
	// THE OWNER IS THE UPSTREAM'S, so the mirror keeps the whole name. That is what lets every
	// cross-repository page work: the panel reads GitHub with the mirror's repository name, and a
	// mirror under a different owner is a page that 404s for a repository that exists.
	branch, commit, err := c.Establish(ctx, owner, name, upstream, "main", upTok)
	if err != nil {
		return "", "", "", err
	}
	// The name is knowable without asking: it is the one that was asked for.
	return owner + "/" + name, branch, commit, nil
}
