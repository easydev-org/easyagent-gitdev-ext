package uihandler

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"

	uiv1 "github.com/easydev-org/easyagent-gitdev-ext/gen/ui/v1"
)

// The account-level RPCs: who the caller is, what they can see, and one repository's releases and
// workflows. None of these name a mirror repository except the last two — which is the point: they are
// how a reader arrives at a repository at all.

func (s *ConnectService) GetWhoami(ctx context.Context, req *uiv1.GetWhoamiRequest) (*uiv1.GetWhoamiResponse, error) {
	if err := need(s.Browse != nil, "repository browsing"); err != nil {
		return nil, err
	}
	login, err := s.Browse.Whoami(ctx)
	if err != nil {
		return nil, internal("whoami", err)
	}
	return &uiv1.GetWhoamiResponse{Login: login}, nil
}

func (s *ConnectService) ListOrgs(ctx context.Context, req *uiv1.ListOrgsRequest) (*uiv1.ListOrgsResponse, error) {
	if err := need(s.Browse != nil, "repository browsing"); err != nil {
		return nil, err
	}
	list, err := s.Browse.Orgs(ctx)
	if err != nil {
		return nil, internal("orgs", err)
	}
	out := make([]*uiv1.OrgSummary, 0, len(list))
	for _, o := range list {
		out = append(out, &uiv1.OrgSummary{Login: o.Login, Description: o.Description})
	}
	return &uiv1.ListOrgsResponse{Orgs: out, Count: int32(len(out))}, nil
}

func (s *ConnectService) ListRepos(ctx context.Context, req *uiv1.ListReposRequest) (*uiv1.ListReposResponse, error) {
	if err := need(s.Browse != nil, "repository browsing"); err != nil {
		return nil, err
	}
	org := req.GetOrg()
	list, err := s.Browse.Repos(ctx, org)
	if err != nil {
		return nil, internal("repos", err)
	}
	out := make([]*uiv1.RepoSummary, 0, len(list))
	for _, r := range list {
		out = append(out, &uiv1.RepoSummary{
			FullName: r.FullName, Description: r.Description, Private: r.Private,
			Archived: r.Archived, Fork: r.Fork, DefaultBranch: r.DefaultBranch,
		})
	}
	// The org is ECHOED BACK, so a caller can tell which filter produced this list.
	return &uiv1.ListReposResponse{Repos: out, Count: int32(len(out)), Org: org}, nil
}

func (s *ConnectService) ListReleases(ctx context.Context, req *uiv1.ListReleasesRequest) (*uiv1.ListReleasesResponse, error) {
	if err := need(s.Browse != nil, "repository browsing"); err != nil {
		return nil, err
	}
	repo := req.GetRepo()
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	// CLAMPED, NOT RESET: asking for 500 gives 100, not the default. A caller that asked for "more"
	// and got "the default" would think the repository had fewer releases than it does.
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	list, err := s.Browse.Releases(ctx, repo, limit)
	if err != nil {
		return nil, internal("releases", err)
	}
	out := make([]*uiv1.ReleaseSummary, 0, len(list))
	for _, r := range list {
		out = append(out, &uiv1.ReleaseSummary{
			Tag: r.Tag, Name: r.Name, Draft: r.Draft, Prerelease: r.Prerelease,
			PublishedAt: r.PublishedAt, Url: r.URL, Body: r.Body, Assets: int32(r.Assets),
		})
	}
	return &uiv1.ListReleasesResponse{Releases: out, Count: int32(len(out))}, nil
}

func (s *ConnectService) ListWorkflows(ctx context.Context, req *uiv1.ListWorkflowsRequest) (*uiv1.ListWorkflowsResponse, error) {
	if err := need(s.Browse != nil, "repository browsing"); err != nil {
		return nil, err
	}
	repo := req.GetRepo()
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	list, err := s.Browse.Workflows(ctx, repo)
	if err != nil {
		return nil, internal("workflows", err)
	}
	out := make([]*uiv1.WorkflowSummary, 0, len(list))
	for _, w := range list {
		out = append(out, &uiv1.WorkflowSummary{Id: w.ID, Name: w.Name, Path: w.Path, State: w.State})
	}
	return &uiv1.ListWorkflowsResponse{Workflows: out, Count: int32(len(out))}, nil
}

// ---------------------------------------------------------------------------
// The mirror
// ---------------------------------------------------------------------------

/**
 * ListMirrored reports which of the named repositories exist in the MIRROR.
 *
 * IT TAKES A LIST, not one name: the answer is a set, and asking per repository would be one round
 * trip per row on a page that shows eighty.
 *
 * A repository that cannot be checked is reported as UNSPECIFIED rather than as an error: the answer's
 * job is to tell a reader which rows are clickable, and a whole page that failed because one name was
 * odd would be worse than one row marked wrong.
 */
func (s *ConnectService) ListMirrored(ctx context.Context, req *uiv1.ListMirroredRequest) (*uiv1.ListMirroredResponse, error) {
	if err := need(s.Mirrored != nil, "the mirror"); err != nil {
		return nil, err
	}
	repos := req.GetRepos()
	// BOUNDED: the page shows tens, and an unbounded list would let one request ask the forge about
	// thousands.
	if len(repos) > 200 {
		repos = repos[:200]
	}
	if len(repos) == 0 {
		return &uiv1.ListMirroredResponse{
			Mirrored: map[string]uiv1.MirrorState{},
		}, nil
	}
	set, err := s.Mirrored.Mirrored(ctx, repos)
	if err != nil {
		return nil, internal("mirrored", err)
	}
	out := make(map[string]uiv1.MirrorState, len(set))
	for name, st := range set {
		switch st {
		case MirrorEmpty:
			out[name] = uiv1.MirrorState_MIRROR_STATE_EMPTY
		case MirrorReady:
			out[name] = uiv1.MirrorState_MIRROR_STATE_READY
		default:
			// MirrorMissing, and anything a source invents later: not mirrored is the safe reading,
			// because it makes the row offer to create one rather than navigate to nothing.
			out[name] = uiv1.MirrorState_MIRROR_STATE_UNSPECIFIED
		}
	}
	return &uiv1.ListMirroredResponse{Mirrored: out}, nil
}

/**
 * Establish creates a mirror of one repository, from its upstream.
 *
 * THE ONLY WRITE IN THIS SERVICE. Everything else here reads; every other change goes through a tool,
 * where the permission model already lives. This one exists because the panel calls it at the moment a
 * reader has said they want a repository — opening a row the mirror does not have — and a tool call
 * there would need the reader to phrase it as an instruction.
 *
 * SLOW BY NATURE: a large repository takes tens of seconds to fetch. The response says what was copied
 * rather than only that it worked, so the page can show the commit that arrived.
 */
func (s *ConnectService) Establish(ctx context.Context, req *uiv1.EstablishRequest) (*uiv1.EstablishResponse, error) {
	if err := need(s.Mirrored != nil, "the mirror"); err != nil {
		return nil, err
	}
	repo := strings.TrimSpace(req.GetRepo())
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	if !strings.Contains(repo, "/") {
		return nil, connect.NewError(connect.CodeInvalidArgument, "repo must be owner/name")
	}
	mirroredAs, branch, commit, err := s.Mirrored.Establish(ctx, repo)
	if err != nil {
		return nil, internal("establish", err)
	}
	return &uiv1.EstablishResponse{
		Repo: repo, MirroredAs: mirroredAs, Branch: branch, Commit: commit,
	}, nil
}
