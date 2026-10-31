package uihandler

import (
	"context"
	"strings"

	"connectrpc.com/connect/v2"

	uiv1 "github.com/easydev-org/easyagent-gitdev-ext/gen/ui/v1"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

// The MIRROR's own pages: browse the repository a change is being made in.
//
// Registered in the same service rather than a separate extension because the panel is one place: a
// reader writing a change looks at a file, and a reader reviewing one looks at a pull request, and they
// are the same person a minute apart. A panel that showed only one of them would send the reader to a
// second extension mid-task.

/**
 * mirrorClient resolves the forge client for the repository a request names, or explains why not.
 *
 * AUTHENTICATION COMES FIRST, BEFORE the repository is even parsed: an unauthenticated caller must not
 * learn whether a repository exists. The tenant comes from the TOKEN and the session from the request,
 * so a session can narrow a tenant's view but never widen it.
 *
 * A FORGE THAT CANNOT BE REACHED IS NOT AN AUTHENTICATION FAILURE. Reporting one as UNAUTHENTICATED
 * would log the reader out over a network fault.
 */
func (s *ConnectService) mirrorClient(ctx context.Context, repo, session string) (*forge.Client, error) {
	if s.Forge == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition,
			"the mirror is not configured in this deployment")
	}
	repo = strings.TrimSpace(repo)
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	// The tenant on the context is what Forge resolves the token from; AuthInterceptor put it there,
	// so this call needs no second authentication.
	tenant, err := tenantFromCtx(ctx)
	if err != nil {
		return nil, err
	}
	c, ferr := s.Forge(ctx, session, tenant, repo)
	if ferr != nil {
		return nil, connect.NewError(connect.CodeUnavailable, ferr.Error())
	}
	return c, nil
}

/**
 * GetRepo describes one mirror repository: its default branch, branches and tags.
 *
 * The repository is ECHOED BACK rather than assumed: the panel is opened with a name in its own URL,
 * and a mistyped or renamed one must be reported as such instead of rendering an empty tree.
 */
func (s *ConnectService) GetRepo(ctx context.Context, req *uiv1.GetRepoRequest) (*uiv1.GetRepoResponse, error) {
	c, err := s.mirrorClient(ctx, req.GetRepo(), "")
	if err != nil {
		return nil, err
	}
	refs, lerr := c.ListRefs(ctx)
	if lerr != nil {
		return nil, forgeErr(lerr)
	}
	// The default branch is the first "branch" the forge lists; a repository with none is EMPTY, and
	// the panel says so rather than offering a branch picker with nothing in it.
	def := ""
	branches := make([]string, 0, len(refs))
	tags := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == "branch" {
			branches = append(branches, ref.Name)
			if def == "" {
				def = ref.Name
			}
		} else {
			tags = append(tags, ref.Name)
		}
	}
	return &uiv1.GetRepoResponse{
		Repo:          strings.Trim(req.GetRepo(), "/"),
		DefaultBranch: def,
		Branches:      branches,
		Tags:          tags,
	}, nil
}

// ListRefs lists everything browsable: every branch and tag.
func (s *ConnectService) ListRefs(ctx context.Context, req *uiv1.ListRefsRequest) (*uiv1.ListRefsResponse, error) {
	c, err := s.mirrorClient(ctx, req.GetRepo(), "")
	if err != nil {
		return nil, err
	}
	refs, lerr := c.ListRefs(ctx)
	if lerr != nil {
		return nil, forgeErr(lerr)
	}
	out := make([]*uiv1.Ref, 0, len(refs))
	for _, ref := range refs {
		out = append(out, &uiv1.Ref{Name: ref.Name, Commit: ref.Commit, Kind: ref.Kind})
	}
	return &uiv1.ListRefsResponse{Refs: out, Count: int32(len(out))}, nil
}

// ListDir lists one directory of the mirror.
func (s *ConnectService) ListDir(ctx context.Context, req *uiv1.ListDirRequest) (*uiv1.ListDirResponse, error) {
	c, err := s.mirrorClient(ctx, req.GetRepo(), "")
	if err != nil {
		return nil, err
	}
	// THE FIELD IS `branch`, because that is what the panel has and what a reader thinks in. The forge
	// calls the same thing a `ref` — a branch, tag or commit — so this is translated once, here, rather
	// than at every call site.
	entries, lerr := c.ListDir(ctx, req.GetDir(), req.GetBranch())
	if lerr != nil {
		return nil, forgeErr(lerr)
	}
	out := make([]*uiv1.TreeEntry, 0, len(entries))
	for _, e := range entries {
		out = append(out, &uiv1.TreeEntry{Path: e.Path, Name: e.Name, Dir: e.Dir, Size: int64(e.Size)})
	}
	return &uiv1.ListDirResponse{
		Dir:     strings.Trim(req.GetDir(), "/"),
		Branch:  req.GetBranch(),
		Entries: out,
		Count:   int32(len(out)),
	}, nil
}

// GetFile returns one file's text.
func (s *ConnectService) GetFile(ctx context.Context, req *uiv1.GetFileRequest) (*uiv1.GetFileResponse, error) {
	c, err := s.mirrorClient(ctx, req.GetRepo(), "")
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(req.GetPath())
	if path == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, "path is required")
	}
	f, ferr := c.ReadFile(ctx, path, req.GetBranch())
	if ferr != nil {
		return nil, forgeErr(ferr)
	}
	return &uiv1.GetFileResponse{
		Path:     f.Path,
		Branch:   req.GetBranch(),
		Revision: f.Revision,
		Content:  f.Content,
		Size:     int64(f.Size),
	}, nil
}
