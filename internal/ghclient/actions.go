package ghclient

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	gogithub "github.com/google/go-github/v93/github"
)

// GitHub Actions: what ran, how far it got, and what it said.
//
// THE HONEST LIMIT, established by probing a live run: GitHub archives a job's log only when the
// job FINISHES. While a run is in progress, `GET /actions/runs/{id}/logs` answers 404. What IS
// live is the STATUS — the run, each job, and each step. So "watch CI" means polling status, and
// the log appears once there is a log.

// Workflow is one Actions workflow defined in the repository.
type Workflow struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// .github/workflows/ci.yml
	Path string `json:"path"`
	// active | disabled_manually | ...
	State string `json:"state"`
}

// ListWorkflows lists the repository's workflows. Paged.
func (c *Client) ListWorkflows(ctx context.Context) ([]Workflow, error) {
	var out []Workflow
	err := paginate(func(page int) (*gogithub.Response, error) {
		wfs, resp, err := c.gh.Actions.ListWorkflows(ctx, c.Owner, c.Repo,
			&gogithub.ListOptions{Page: page, PerPage: 100})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, w := range wfs.Workflows {
			out = append(out, Workflow{
				ID: w.GetID(), Name: w.GetName(), Path: w.GetPath(), State: w.GetState(),
			})
		}
		return resp, nil
	})
	return out, err
}

// Run is one execution of a workflow.
type Run struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// queued | in_progress | completed
	Status string `json:"status"`
	// success | failure | cancelled | ... (empty while running)
	Conclusion string `json:"conclusion"`
	Branch     string `json:"branch"`
	// push | pull_request | workflow_dispatch | ...
	Event     string `json:"event"`
	HeadSHA   string `json:"headSha"`
	URL       string `json:"url"`
	CreatedAt string `json:"createdAt"`
	// Attempt distinguishes a re-run of the same workflow at the same sha.
	Attempt int `json:"attempt"`
}

// ListRunsOptions filters the run list.
type ListRunsOptions struct {
	// Branch restricts to runs on one branch.
	Branch string
	// Event restricts to one trigger (push, pull_request, workflow_dispatch).
	Event string
	// Limit caps how many are returned (default 30).
	Limit int
}

// ListRuns lists workflow runs, newest first. Paged.
func (c *Client) ListRuns(ctx context.Context, o ListRunsOptions) ([]Run, error) {
	if o.Limit <= 0 {
		o.Limit = 30
	}
	var out []Run
	err := paginate(func(page int) (*gogithub.Response, error) {
		res, resp, err := c.gh.Actions.ListRepositoryWorkflowRuns(ctx, c.Owner, c.Repo,
			&gogithub.ListWorkflowRunsOptions{
				Branch:      o.Branch,
				Event:       o.Event,
				ListOptions: gogithub.ListOptions{Page: page, PerPage: 100},
			})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, r := range res.WorkflowRuns {
			if len(out) >= o.Limit {
				break
			}
			out = append(out, runFromG(r))
		}
		return resp, nil
	})
	if err != nil {
		return nil, err
	}
	if len(out) > o.Limit {
		out = out[:o.Limit]
	}
	return out, nil
}

func runFromG(r *gogithub.WorkflowRun) Run {
	return Run{
		ID: r.GetID(), Name: r.GetName(), Status: r.GetStatus(),
		Conclusion: r.GetConclusion(), Branch: r.GetHeadBranch(), Event: r.GetEvent(),
		HeadSHA: r.GetHeadSHA(), URL: r.GetHTMLURL(),
		CreatedAt: r.GetCreatedAt().Format("2006-01-02 15:04:05"),
		Attempt:   r.GetRunAttempt(),
	}
}

// Step is one step in a job.
type Step struct {
	Number     int64  `json:"number"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	// StartedAt / CompletedAt are empty until the step begins / ends.
	StartedAt   string `json:"startedAt"`
	CompletedAt string `json:"completedAt"`
}

// Job is one job in a run.
type Job struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	StartedAt  string `json:"startedAt"`
	Steps      []Step `json:"steps"`
	URL        string `json:"url"`
}

/**
 * Run returns ONE run.
 *
 * A page opened on a specific run needs that run, and listing the repository's runs to find it would
 * cost a paginated request per page — and get slower as the repository gets busier, for a lookup
 * that GitHub offers directly.
 */
func (c *Client) Run(ctx context.Context, runID int64) (Run, error) {
	r, resp, err := c.gh.Actions.GetWorkflowRunByID(ctx, c.Owner, c.Repo, runID)
	c.note(resp)
	if err != nil {
		return Run{}, err
	}
	return runFromG(r), nil
}

// RunJobs lists the jobs of a run with their per-step status.
//
// This is the LIVE view: while a run is in progress, `status` is in_progress and the step that
// is currently executing says so. The job's LOG is not available until the job completes, so
// this is what a caller polls to see where a run has got to.
//
// Paged, because a matrix build can produce more jobs than one page.
func (c *Client) RunJobs(ctx context.Context, runID int64) ([]Job, error) {
	var out []Job
	err := paginate(func(page int) (*gogithub.Response, error) {
		jobs, resp, err := c.gh.Actions.ListWorkflowJobs(ctx, c.Owner, c.Repo, runID,
			&gogithub.ListWorkflowJobsOptions{ListOptions: gogithub.ListOptions{Page: page, PerPage: 100}})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, j := range jobs.Jobs {
			job := Job{
				ID: j.GetID(), Name: j.GetName(), Status: j.GetStatus(),
				Conclusion: j.GetConclusion(), URL: j.GetHTMLURL(),
			}
			// StartedAt is nil for a job that has not started yet. A freshly dispatched run has
			// jobs in `queued` with no start time, so this is the common case, not an edge one.
			//
			// The guard is `GetTime() != nil`, NOT `!j.StartedAt.IsZero()`: Timestamp embeds
			// time.Time and IsZero has a VALUE receiver, so calling it through a nil pointer
			// dereferences and panics. GetTime is nil-safe, and it is the only safe accessor here.
			if p := j.StartedAt.GetTime(); p != nil {
				job.StartedAt = p.Format("15:04:05")
			}
			for _, s := range j.Steps {
				step := Step{
					Number: s.GetNumber(), Name: s.GetName(),
					Status: s.GetStatus(), Conclusion: s.GetConclusion(),
				}
				if p := s.StartedAt.GetTime(); p != nil {
					step.StartedAt = p.Format("15:04:05")
				}
				if p := s.CompletedAt.GetTime(); p != nil {
					step.CompletedAt = p.Format("15:04:05")
				}
				job.Steps = append(job.Steps, step)
			}
			out = append(out, job)
		}
		return resp, nil
	})
	return out, err
}

// RunLog is one job's log, extracted from the archive GitHub provides.
type RunLog struct {
	Job   string
	Lines []string
	// Truncated is true when the log was cut to the caller's limit.
	Truncated bool
}

// RunLogs downloads a run's logs and returns them per job.
//
// GitHub serves ONE zip per run, containing `{job name}/{step}.txt` files. It is downloaded
// from a short-lived redirect, so the archive is fetched here rather than exposing a URL that
// would be dead by the time a caller used it.
//
// A run that has not finished has NO logs: GitHub answers 404 until the job completes. That is
// reported as such, because "no log yet" and "the download failed" lead to different actions.
//
// maxLinesPerJob caps the returned text per job; 0 means no cap.
func (c *Client) RunLogs(ctx context.Context, runID int64, maxLinesPerJob int) ([]RunLog, error) {
	u, resp, err := c.gh.Actions.GetWorkflowRunLogs(ctx, c.Owner, c.Repo, runID, 10)
	c.note(resp)
	if err != nil {
		if IsNotFound(err) {
			return nil, fmt.Errorf("no logs for run %d yet: GitHub only publishes a job's log once the job "+
				"FINISHES. There is no API for a running job's output — the live signal is the step STATUS: "+
				"poll gh-run-jobs to see which step is executing and how long it has been there", runID)
		}
		return nil, wrap(err)
	}

	// The URL is pre-signed and expires; fetch it now.
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	// A plain client: the URL is pre-signed, so it must NOT carry the GitHub Authorization
	// header — sending it to the storage backend would be rejected.
	archiveResp, err := (&http.Client{Timeout: c.httpTimeout}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("download logs: %w", err)
	}
	defer func() { _ = archiveResp.Body.Close() }()
	if archiveResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download logs: HTTP %d", archiveResp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(archiveResp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}

	phase(ctx, "unpacking the log archive")
	zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, fmt.Errorf("logs are not a zip archive: %w", err)
	}

	// Group the archive's files by job: "build/1_setup.txt" belongs to job "build".
	byJob := map[string][]*zip.File{}
	for _, f := range zr.File {
		name := f.Name
		parts := strings.SplitN(strings.TrimPrefix(name, "/"), "/", 2)
		if len(parts) < 2 || strings.HasSuffix(name, "/") {
			continue
		}
		byJob[parts[0]] = append(byJob[parts[0]], f)
	}

	jobs := make([]string, 0, len(byJob))
	for j := range byJob {
		jobs = append(jobs, j)
	}
	sort.Strings(jobs)

	out := make([]RunLog, 0, len(jobs))
	for _, job := range jobs {
		files := byJob[job]
		// Step files are numbered; sort by name so the log reads in execution order.
		sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })

		var lines []string
		truncated := false
		for _, f := range files {
			rc, oerr := f.Open()
			if oerr != nil {
				continue
			}
			body, rerr := io.ReadAll(io.LimitReader(rc, 8<<20))
			_ = rc.Close()
			if rerr != nil {
				continue
			}
			for _, ln := range strings.Split(strings.TrimRight(string(body), "\n"), "\n") {
				if maxLinesPerJob > 0 && len(lines) >= maxLinesPerJob {
					truncated = true
					break
				}
				lines = append(lines, ln)
			}
			if truncated {
				break
			}
		}
		out = append(out, RunLog{Job: job, Lines: lines, Truncated: truncated})
	}
	return out, nil
}
