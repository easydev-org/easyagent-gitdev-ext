package uihandler

import (
	"context"
	"errors"

	"connectrpc.com/connect/v2"

	uiv1 "github.com/easydev-org/easyagent-gitdev-ext/gen/ui/v1"
)

// The UPSTREAM's pages: pull requests, issues and CI runs.
//
// These are the only calls here that cost rate limit, and the only ones that read a REMOTE — the rest
// of the panel reads the local mirror, which is free. That is also why they are the ones a card cannot
// replace: a card replays the snapshot a tool returned, while a page is somewhere the reader goes to
// see the CURRENT state. Somebody may have pushed since the tool ran.

// ErrLogNotReady means the job has not finished, so the forge has published no log for it.
var ErrLogNotReady = errors.New("this job has not finished, so its log is not available yet")

// pullCtx authenticates and validates the repository and number every pull-request call needs.
func (s *ConnectService) pullCtx(ctx context.Context, repo string, number int32, what string) (context.Context, error) {
	// THE TENANT IS ALREADY ON THE CONTEXT: AuthInterceptor resolved it before this method ran.
	if _, err := tenantFromCtx(ctx); err != nil {
		return ctx, err
	}
	if err := need(s.Pulls != nil, "pull requests"); err != nil {
		return ctx, err
	}
	if err := requireRepo(repo); err != nil {
		return ctx, err
	}
	if err := requirePositive(what, int64(number)); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func (s *ConnectService) GetPull(ctx context.Context, req *uiv1.GetPullRequest) (*uiv1.GetPullResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.pullCtx(ctx, repo, n, "number (the pull request id)")
	if err != nil {
		return nil, err
	}
	v, perr := s.Pulls.Pull(ctx, repo, int(n))
	if perr != nil {
		return nil, internal("pull", perr)
	}
	return &uiv1.GetPullResponse{Pull: &uiv1.PullSummary{
		Number: int32(v.Number), Title: v.Title, State: v.State, Author: v.Author, Body: v.Body,
		Head: v.Head, Base: v.Base, Url: v.URL, Draft: v.Draft, Merged: v.Merged, Created: v.Created,
	}}, nil
}

func (s *ConnectService) ListPullFiles(ctx context.Context, req *uiv1.ListPullFilesRequest) (*uiv1.ListPullFilesResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.pullCtx(ctx, repo, n, "number (the pull request id)")
	if err != nil {
		return nil, err
	}
	v, perr := s.Pulls.Files(ctx, repo, int(n))
	if perr != nil {
		return nil, internal("pull files", perr)
	}
	out := make([]*uiv1.FileChange, 0, len(v))
	for _, f := range v {
		out = append(out, &uiv1.FileChange{
			Path: f.Path, Status: f.Status, Additions: int32(f.Additions),
			Deletions: int32(f.Deletions), Patch: f.Patch, PatchTruncated: f.PatchTruncated,
		})
	}
	return &uiv1.ListPullFilesResponse{Files: out, Count: int32(len(out))}, nil
}

func (s *ConnectService) GetPullDiff(ctx context.Context, req *uiv1.GetPullDiffRequest) (*uiv1.GetPullDiffResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.pullCtx(ctx, repo, n, "number (the pull request id)")
	if err != nil {
		return nil, err
	}
	text, perr := s.Pulls.Diff(ctx, repo, int(n))
	if perr != nil {
		return nil, internal("pull diff", perr)
	}
	return &uiv1.GetPullDiffResponse{Diff: text}, nil
}

func (s *ConnectService) GetPullChecks(ctx context.Context, req *uiv1.GetPullChecksRequest) (*uiv1.GetPullChecksResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.pullCtx(ctx, repo, n, "number (the pull request id)")
	if err != nil {
		return nil, err
	}
	v, perr := s.Pulls.Checks(ctx, repo, int(n))
	if perr != nil {
		return nil, internal("pull checks", perr)
	}
	out := make([]*uiv1.CheckRow, 0, len(v.Checks))
	for _, c := range v.Checks {
		out = append(out, &uiv1.CheckRow{Name: c.Name, Status: c.Status})
	}
	return &uiv1.GetPullChecksResponse{
		State: v.State, Sha: v.HeadSHA, Checks: out,
	}, nil
}

// ---------------------------------------------------------------------------
// Issues
// ---------------------------------------------------------------------------

// issueCtx authenticates and validates the repository and number an issue call needs.
//
// A repo and a number are a repo and a number whichever kind of item the number names, so this shares
// its shape with pullCtx — but NOT its nil check: a deployment can have issues wired without pulls.
func (s *ConnectService) issueCtx(ctx context.Context, repo string, number int32) (context.Context, error) {
	// THE TENANT IS ALREADY ON THE CONTEXT: AuthInterceptor resolved it before this method ran.
	if _, err := tenantFromCtx(ctx); err != nil {
		return ctx, err
	}
	if err := need(s.Issues != nil, "issues"); err != nil {
		return ctx, err
	}
	if err := requireRepo(repo); err != nil {
		return ctx, err
	}
	if err := requirePositive("number (the issue id)", int64(number)); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func issueProto(v IssueView) *uiv1.IssueSummary {
	return &uiv1.IssueSummary{
		Number: int32(v.Number), Title: v.Title, State: v.State, Body: v.Body, Author: v.Author,
		Url: v.URL, Labels: v.Labels, IsPullRequest: v.IsPullRequest, Comments: int32(v.Comments),
	}
}

func (s *ConnectService) GetIssue(ctx context.Context, req *uiv1.GetIssueRequest) (*uiv1.GetIssueResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.issueCtx(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	v, ierr := s.Issues.Issue(ctx, repo, int(n))
	if ierr != nil {
		return nil, internal("issue", ierr)
	}
	return &uiv1.GetIssueResponse{Issue: issueProto(v)}, nil
}

func (s *ConnectService) ListIssueComments(ctx context.Context, req *uiv1.ListIssueCommentsRequest) (*uiv1.ListIssueCommentsResponse, error) {
	repo, n := req.GetRepo(), req.GetNumber()
	ctx, err := s.issueCtx(ctx, repo, n)
	if err != nil {
		return nil, err
	}
	v, ierr := s.Issues.Comments(ctx, repo, int(n))
	if ierr != nil {
		return nil, internal("issue comments", ierr)
	}
	out := make([]*uiv1.Comment, 0, len(v))
	for _, c := range v {
		out = append(out, &uiv1.Comment{Id: c.ID, Author: c.Author, Body: c.Body, When: c.When})
	}
	return &uiv1.ListIssueCommentsResponse{Comments: out, Count: int32(len(out))}, nil
}

/**
 * ListIssues lists a repository's issues or pull requests.
 *
 * `kind` IS REFUSED WHEN IT IS NEITHER, rather than ignored: a filter that silently does nothing looks
 * like it worked, and a caller would believe they were reading pull requests while reading issues.
 * EMPTY MEANS "issue", which is what the route this replaces did.
 */
func (s *ConnectService) ListIssues(ctx context.Context, req *uiv1.ListIssuesRequest) (*uiv1.ListIssuesResponse, error) {
	if err := need(s.Issues != nil, "issues"); err != nil {
		return nil, err
	}
	repo := req.GetRepo()
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	kind := req.GetKind()
	switch kind {
	case "issue", "pr":
	case "":
		kind = "issue"
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, `kind must be "issue" or "pr"`)
	}
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 30
	}
	list, lerr := s.Issues.List(ctx, repo, kind, req.GetState(), limit)
	if lerr != nil {
		return nil, internal("issues", lerr)
	}
	out := make([]*uiv1.IssueSummary, 0, len(list))
	for _, v := range list {
		out = append(out, issueProto(v))
	}
	return &uiv1.ListIssuesResponse{
		Items: out, Count: int32(len(out)), Kind: kind,
	}, nil
}

// ---------------------------------------------------------------------------
// CI runs
// ---------------------------------------------------------------------------

func (s *ConnectService) runCtx(ctx context.Context, repo string, id int64, what string) (context.Context, error) {
	// THE TENANT IS ALREADY ON THE CONTEXT: AuthInterceptor resolved it before this method ran.
	if _, err := tenantFromCtx(ctx); err != nil {
		return ctx, err
	}
	if err := need(s.Runs != nil, "CI runs"); err != nil {
		return ctx, err
	}
	if err := requireRepo(repo); err != nil {
		return ctx, err
	}
	if err := requirePositive(what, id); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func runProto(v RunView) *uiv1.RunSummary {
	return &uiv1.RunSummary{
		Id: v.ID, Name: v.Name, Status: v.Status, Conclusion: v.Conclusion, Branch: v.Branch,
		Event: v.Event, HeadSha: v.HeadSHA, Url: v.URL, CreatedAt: v.CreatedAt,
		Attempt: int32(v.Attempt), Done: v.Done,
	}
}

func (s *ConnectService) ListRuns(ctx context.Context, req *uiv1.ListRunsRequest) (*uiv1.ListRunsResponse, error) {
	if err := need(s.Runs != nil, "CI runs"); err != nil {
		return nil, err
	}
	repo := req.GetRepo()
	if err := requireRepo(repo); err != nil {
		return nil, err
	}
	limit := int(req.GetLimit())
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	list, lerr := s.Runs.Runs(ctx, repo, limit)
	if lerr != nil {
		return nil, internal("runs", lerr)
	}
	out := make([]*uiv1.RunSummary, 0, len(list))
	for _, v := range list {
		out = append(out, runProto(v))
	}
	return &uiv1.ListRunsResponse{Runs: out, Count: int32(len(out))}, nil
}

func (s *ConnectService) GetRun(ctx context.Context, req *uiv1.GetRunRequest) (*uiv1.GetRunResponse, error) {
	repo, id := req.GetRepo(), req.GetId()
	ctx, err := s.runCtx(ctx, repo, id, "run")
	if err != nil {
		return nil, err
	}
	v, rerr := s.Runs.Run(ctx, repo, id)
	if rerr != nil {
		return nil, internal("run", rerr)
	}
	return &uiv1.GetRunResponse{Run: runProto(v)}, nil
}

func (s *ConnectService) ListRunJobs(ctx context.Context, req *uiv1.ListRunJobsRequest) (*uiv1.ListRunJobsResponse, error) {
	repo, id := req.GetRepo(), req.GetId()
	ctx, err := s.runCtx(ctx, repo, id, "run")
	if err != nil {
		return nil, err
	}
	list, rerr := s.Runs.Jobs(ctx, repo, id)
	if rerr != nil {
		return nil, internal("run jobs", rerr)
	}
	out := make([]*uiv1.JobSummary, 0, len(list))
	for _, j := range list {
		steps := make([]*uiv1.StepSummary, 0, len(j.Steps))
		for _, st := range j.Steps {
			steps = append(steps, &uiv1.StepSummary{
				Name: st.Name, Status: st.Status, Conclusion: st.Conclusion, Number: int32(st.Number),
			})
		}
		out = append(out, &uiv1.JobSummary{
			Id: j.ID, Name: j.Name, Status: j.Status, Conclusion: j.Conclusion,
			StartedAt: j.StartedAt, Url: j.URL, Steps: steps, LogAvailable: j.LogAvailable,
		})
	}
	return &uiv1.ListRunJobsResponse{Jobs: out, Count: int32(len(out))}, nil
}

/**
 * GetJobLog returns ONE job's log.
 *
 * IT IS ITS OWN RPC because that is the whole point: the page lists jobs without their logs, and the
 * reader opens the one they care about. A log that is not there yet — a job still running — is
 * reported as UNAVAILABLE rather than as an empty success, so the panel can say "not yet" instead of
 * "nothing", which are different facts.
 */
func (s *ConnectService) GetJobLog(ctx context.Context, req *uiv1.GetJobLogRequest) (*uiv1.GetJobLogResponse, error) {
	repo, id := req.GetRepo(), req.GetJobId()
	ctx, err := s.runCtx(ctx, repo, id, "job")
	if err != nil {
		return nil, err
	}
	text, rerr := s.Runs.JobLog(ctx, repo, id)
	if rerr != nil {
		if errors.Is(rerr, ErrLogNotReady) {
			// UNAVAILABLE, not INTERNAL: the request is valid and the resource is not ready, which a
			// reader can act on ("wait", or "reload") where a 500 would just look broken.
			return nil, connect.NewError(connect.CodeUnavailable, rerr.Error())
		}
		return nil, internal("job log", rerr)
	}
	return &uiv1.GetJobLogResponse{Log: text, Bytes: int32(len(text))}, nil
}
