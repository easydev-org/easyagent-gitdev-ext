package ghclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v93/github"
)

// readAllLimited reads up to limit bytes.
//
// Separate from io.ReadAll(io.LimitReader(...)) because that hides the difference between "the
// body was exactly limit bytes" and "the body was longer and got cut". The caller passes limit+1
// and compares, so an oversized download is reported instead of silently shortened.
func readAllLimited(r io.Reader, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r, limit))
}

// Artifacts, and the operations that CHANGE a run.
//
// The read tools answer "what happened"; these answer "do it again" and "stop it". They are
// separated because they are the first things here that write: seeing a failure and being unable
// to rerun it makes the CI surface a report, not a tool.

// Artifact is one uploaded artifact.
type Artifact struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	Expired bool   `json:"expired"`
	// Digest is the SHA256 of the archive, set for uploads from upload-artifact v4 on. Empty for
	// older ones, which is why it is not used as an identity.
	Digest    string `json:"digest"`
	CreatedAt string `json:"createdAt"`
	ExpiresAt string `json:"expiresAt"`
	// DownloadURL is the API redirect; the archive is fetched through it. json:"-" because it is a
	// CREDENTIAL-adjacent redirect target, not a fact about the artifact.
	DownloadURL string `json:"-"`
}

// ListArtifacts lists the artifacts of one run. Paged.
func (c *Client) ListArtifacts(ctx context.Context, runID int64) ([]Artifact, error) {
	var out []Artifact
	err := paginate(func(page int) (*gogithub.Response, error) {
		list, resp, err := c.gh.Actions.ListWorkflowRunArtifacts(ctx, c.Owner, c.Repo, runID,
			&gogithub.ListOptions{Page: page, PerPage: 100})
		c.note(resp)
		if err != nil {
			return resp, err
		}
		for _, a := range list.Artifacts {
			out = append(out, Artifact{
				ID:          a.GetID(),
				Name:        a.GetName(),
				Size:        a.GetSizeInBytes(),
				Expired:     a.GetExpired(),
				Digest:      a.GetDigest(),
				CreatedAt:   a.GetCreatedAt().Format("2006-01-02 15:04"),
				ExpiresAt:   a.GetExpiresAt().Format("2006-01-02 15:04"),
				DownloadURL: a.GetArchiveDownloadURL(),
			})
		}
		return resp, nil
	})
	return out, err
}

// DownloadArtifact fetches one artifact archive as bytes.
//
// THE REDIRECT IS THE TRAP, again: the API answers 302 with a pre-signed URL, and a pre-signed
// URL REJECTS a request carrying an Authorization header. So the Location is fetched separately
// with a plain client. (The same mistake produced a phantom 401 on job logs; see joblog.go.)
//
// maxBytes bounds the download. An artifact can be gigabytes, and this returns bytes in memory —
// so the cap is a guard against the process being killed by an artifact it was only asked to
// inspect, and it is reported rather than silently truncating.
func (c *Client) DownloadArtifact(ctx context.Context, artifactID int64, maxBytes int64) ([]byte, error) {
	u, resp, err := c.gh.Actions.DownloadArtifact(ctx, c.Owner, c.Repo, artifactID, 10)
	c.note(resp)
	if err != nil {
		return nil, wrap(err)
	}
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return nil, err
	}
	// No Authorization header: the URL is pre-signed.
	client := &http.Client{Timeout: 5 * time.Minute}
	ar, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download artifact: %w", err)
	}
	defer func() { _ = ar.Body.Close() }()
	if ar.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download artifact: HTTP %d", ar.StatusCode)
	}
	if maxBytes <= 0 {
		maxBytes = 64 << 20
	}
	// Read one byte past the cap so "exactly at the cap" is distinguishable from "more".
	data, err := readAllLimited(ar.Body, maxBytes+1)
	if err != nil {
		return nil, fmt.Errorf("read artifact: %w", err)
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("artifact %d is larger than the %d-byte limit; raise `max_bytes` or "+
			"download it from the run page", artifactID, maxBytes)
	}
	return data, nil
}

// RerunRun reruns a whole workflow run.
func (c *Client) RerunRun(ctx context.Context, runID int64) error {
	resp, err := c.gh.Actions.RerunWorkflowByID(ctx, c.Owner, c.Repo, runID)
	c.note(resp)
	return rerunError(err)
}

// RerunFailedJobs reruns only the failed jobs of a run.
//
// Preferred over RerunRun for a failure: the jobs that passed are kept, so a flaky test costs one
// job rather than the whole workflow.
func (c *Client) RerunFailedJobs(ctx context.Context, runID int64) error {
	resp, err := c.gh.Actions.RerunFailedJobsByID(ctx, c.Owner, c.Repo, runID)
	c.note(resp)
	return rerunError(err)
}

// rerunError turns GitHub's refusal into something actionable.
//
// Two refusals are common and both are correct behaviour, not failures:
//   - 403 "This workflow is already running"   — a rerun cannot start while the run is live
//   - 403 "not rerunnable" / 409               — nothing failed, or the run never completed
//
// Returning the raw text makes a caller think it lacks permission. These are sentences instead.
func rerunError(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case containsFold(msg, "already running"):
		return fmt.Errorf("cannot rerun: this workflow is still running. A rerun starts a new attempt " +
			"and GitHub refuses to begin one over a live run — wait for it to finish (gh-run-jobs), " +
			"or cancel it first (gh-run-cancel)")
	// GitHub calls "nothing to rerun" a run that "cannot be retried" — a phrase that does
	// not contain "rerun" at all, which is why the first attempt at this did not match.
	case containsFold(msg, "cannot be retried"),
		containsFold(msg, "not rerunnable"),
		containsFold(msg, "no failed"):
		return fmt.Errorf("cannot rerun: there is nothing to rerun — GitHub only reruns a COMPLETED run, " +
			"and this one either has no failed jobs or never finished. Check its state with gh-run-jobs")
	}
	return wrap(err)
}

// CancelRun cancels a run that is in progress.
func (c *Client) CancelRun(ctx context.Context, runID int64) error {
	resp, err := c.gh.Actions.CancelWorkflowRunByID(ctx, c.Owner, c.Repo, runID)
	c.note(resp)
	if err == nil {
		return nil
	}
	// "job scheduled on GitHub side; try again later" means the run exists but no runner has
	// picked it up yet, so there is nothing to cancel — the message reads like a transient error
	// and invites a pointless retry.
	msg := err.Error()
	if containsFold(msg, "try again later") {
		return fmt.Errorf("cannot cancel yet: the run is queued and no runner has picked it up. " +
			"There is nothing running to stop, and the message is not a temporary failure — " +
			"check the state with gh-run-jobs, and cancel once a job reports in_progress")
	}
	// "Cannot cancel a workflow run that is completed" — nothing to stop, and retrying cannot
	// help, so say that rather than surfacing the API text.
	if containsFold(msg, "completed") {
		return fmt.Errorf("cannot cancel: the run has already completed. Nothing is running, so " +
			"there is nothing to stop — see its result with gh-run-jobs")
	}
	return wrap(err)
}

// containsFold is a case-insensitive substring test, for matching GitHub's prose.
func containsFold(s, sub string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(sub))
}

// DispatchOptions describes a manual workflow trigger.
type DispatchOptions struct {
	// Workflow is the file name (ci.yml) or the numeric id as a string.
	Workflow string
	// Ref is the branch or tag to run on. Required by GitHub.
	Ref string
	// Inputs are the workflow's declared inputs.
	Inputs map[string]any
}

// DispatchRun triggers a workflow_dispatch event and returns the run it created.
//
// ReturnRunDetails is requested so the caller learns the run id: without it the response is 204
// with no body, and "did it start, and which run was it?" would have to be answered by polling
// the run list and guessing by timestamp.
func (c *Client) DispatchRun(ctx context.Context, o DispatchOptions) (int64, string, error) {
	if o.Ref == "" {
		return 0, "", fmt.Errorf("ref is required: GitHub will not dispatch a workflow without a branch or tag")
	}
	details := true
	req := gogithub.CreateWorkflowDispatchEventRequest{
		Ref: o.Ref, Inputs: o.Inputs, ReturnRunDetails: &details,
	}
	res, resp, err := c.gh.Actions.CreateWorkflowDispatchEventByFileName(
		ctx, c.Owner, c.Repo, o.Workflow, req)
	c.note(resp)
	if err != nil {
		return 0, "", wrap(err)
	}
	if res == nil {
		return 0, "", nil
	}
	return res.GetWorkflowRunID(), res.GetHTMLURL(), nil
}
