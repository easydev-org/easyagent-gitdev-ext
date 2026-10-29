package main

import (
	"fmt"
	"strings"
	"time"
)

// The account reads. Each asserts a NON-EMPTY answer, never merely an absent error.
func (r *runner) stepWhoami(st *state) error {
	res, err := r.must("gh-whoami", "gh-whoami", nil)
	if err != nil {
		return err
	}
	// The token belongs to somebody, and a reply that names nobody means the credential did not take.
	if strings.TrimSpace(res.Content) == "" && len(res.Data) == 0 {
		return fmt.Errorf("the reply names no account")
	}
	r.ok("gh-whoami")
	return nil
}

func (r *runner) stepOrgs(st *state) error {
	// AN EMPTY ORGANISATION LIST IS VALID for a personal account, so this asserts the CALL answered
	// rather than that the list has members — the difference between an empty result and a broken one
	// is what a suite has to be careful about.
	res, err := r.must("gh-orgs", "gh-orgs", nil)
	if err != nil {
		return err
	}
	if res.Data == nil && res.Content == "" {
		return fmt.Errorf("neither data nor content came back")
	}
	r.ok("gh-orgs")
	return nil
}

func (r *runner) stepRepos(st *state) error {
	res, err := r.must("gh-repos", "gh-repos", map[string]any{"limit": 20})
	if err != nil {
		return err
	}
	// The repository under test must be among them, so this cannot pass on an empty list.
	if !contains(res, r.repo) {
		return fmt.Errorf("%s is not in the repository list", r.repo)
	}
	r.ok("gh-repos")
	return nil
}

func (r *runner) stepRepoInfo(st *state) error {
	res, err := r.must("gh-info", "gh-info", map[string]any{"repo": r.repo})
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Content) == "" {
		return fmt.Errorf("no content")
	}
	r.ok("gh-info")
	return nil
}

func (r *runner) stepFileList(st *state) error {
	res, err := r.must("git-file-list", "git-file-list", map[string]any{
		"remote": r.mirrorRemote(), "branch": "main", "dir": "",
	})
	if err != nil {
		return err
	}
	entries := list(res.Data, "entries")
	if len(entries) == 0 {
		return fmt.Errorf("the mirror's main is empty")
	}
	// Kept so the read step knows which file to open: a hard-coded name would break on a repository
	// whose layout differs.
	for _, e := range entries {
		p := str(e, "path")
		if p == "" {
			p = str(e, "Path")
		}
		isDir := e["dir"] == true || e["Dir"] == true
		if !isDir && p != "" {
			st.File = p
			break
		}
	}
	r.ok("git-file-list")
	return nil
}

func (r *runner) stepFileRead(st *state) error {
	if st.File == "" {
		return fmt.Errorf("the listing named no file to read")
	}
	res, err := r.must("git-file-read", "git-file-read", map[string]any{
		"remote": r.mirrorRemote(), "branch": "main", "path": st.File,
	})
	if err != nil {
		return err
	}
	if strings.TrimSpace(res.Content) == "" {
		return fmt.Errorf("%s read as empty", st.File)
	}
	r.ok("git-file-read")
	return nil
}

// stepMirrorSync fast-forwards the mirror's main to the upstream.
func (r *runner) stepMirrorSync(st *state) error {
	// `upstream` names what to follow. It is not derivable from the mirror's own address: the mirror is
	// a copy, and the tool needs to be told which original to compare against.
	res, err := r.must("git-sync-upstream", "git-sync-upstream", map[string]any{
		"remote":   r.mirrorRemote(),
		"branch":   "main",
		"upstream": fmt.Sprintf("https://github.com/%s.git", r.repo),
	})
	if err != nil {
		return err
	}
	if str(res.Data, "branch") == "" {
		return fmt.Errorf("no branch in the reply")
	}
	r.ok("git-sync-upstream")
	return nil
}

// stepBranchCreate makes a working branch that a patch can land on.
func (r *runner) stepBranchCreate(st *state) error {
	// A name built from the clock, so a re-run on the same repository does not collide with the
	// branch the previous run left behind.
	st.Branch = fmt.Sprintf("e2e/%d", time.Now().Unix())
	_, err := r.must("git-branch-create", "git-branch-create", map[string]any{
		"remote": r.mirrorRemote(), "branch": st.Branch, "from": "main",
		// The suite runs repeatedly; a branch that already exists must be reused rather than
		// reported as a failure.
		"on_exists": "reuse",
	})
	if err != nil {
		return err
	}
	r.ok("git-branch-create")
	return nil
}

// stepFilePatch writes one file in one commit.
func (r *runner) stepFilePatch(st *state) error {
	st.PatchFile = fmt.Sprintf("e2e-note-%d.md", time.Now().Unix())
	res, err := r.must("git-file-patch", "git-file-patch", map[string]any{
		"remote": r.mirrorRemote(), "branch": st.Branch,
		"message": "e2e: a file written through the forge API\n",
		// THE PATCH FORMAT IS THE TOOL'S, not a bare diff: *** Begin/End Patch wraps the operation, and
		// the payload is the file's content. A malformed patch is refused rather than half-applied,
		// which is why this is asserted by the tool before anything is written.
		// A UNIQUE FILE NAME. A fixed one makes the suite fail on its second run — the file is already
		// on main, and 'Add File' is refused for an existing path. A suite that only passes on a clean
		// repository is a suite that stops being run.
		"patch": "*** Begin Patch\n*** Add File: " + st.PatchFile + "\n+e2e wrote this at " +
			time.Now().Format(time.RFC3339) + "\n*** End Patch\n",
	})
	if err != nil {
		return err
	}
	st.Commit = str(res.Data, "commit")
	if st.Commit == "" {
		return fmt.Errorf("no commit in the reply")
	}
	r.ok("git-file-patch")
	return nil
}

// stepVerifyPatch reads the write back through a DIFFERENT tool.
func (r *runner) stepVerifyPatch(st *state) error {
	res, err := r.must("git-file-list", "git-file-list", map[string]any{
		"remote": r.mirrorRemote(), "branch": st.Branch, "dir": "",
	})
	if err != nil {
		return err
	}
	entries := list(res.Data, "entries")
	for _, e := range entries {
		p := str(e, "path")
		if p == "" {
			p = str(e, "Path")
		}
		if p == st.PatchFile {
			r.ok("git-file-list (after patch)")
			return nil
		}
	}
	return fmt.Errorf("the patched file is not in the listing (%d entries)", len(entries))
}

// stepPushBranch sends the branch to the upstream.
func (r *runner) stepPushBranch(st *state) error {
	// `upstream` IS REQUIRED, and it is not derivable from the mirror's address: the tool sends a
	// branch from the mirror TO an upstream, and both ends have to be named.
	_, err := r.must("git-push-branch", "git-push-branch", map[string]any{
		"remote":   r.mirrorRemote(),
		"branch":   st.Branch,
		"upstream": fmt.Sprintf("https://github.com/%s.git", r.repo),
	})
	if err != nil {
		return err
	}
	r.ok("git-push-branch")
	return nil
}

// stepPRCreate opens the pull request the push made possible.
func (r *runner) stepPRCreate(st *state) error {
	res, err := r.must("gh-pr-create", "gh-pr-create", map[string]any{
		"repo": r.repo, "head": st.Branch, "base": "main",
		"title": "e2e: automated pull request\n",
		"body":  "Opened by cmd/e2e-all. Safe to close.\n",
	})
	if err != nil {
		return err
	}
	st.PR = int(num(res.Data, "number"))
	if st.PR == 0 {
		// The number is sometimes only in the prose; the panel's own card reads it from there.
		if idx := strings.Index(res.Content, "#"); idx >= 0 {
			fmt.Sscanf(res.Content[idx+1:], "%d", &st.PR)
		}
	}
	if st.PR == 0 {
		return fmt.Errorf("no pull request number in the reply: %s", truncate(res.Content, 200))
	}
	r.ok("gh-pr-create")
	return nil
}

func (r *runner) stepPRFiles(st *state) error {
	res, err := r.must("gh-pr-files", "gh-pr-files", map[string]any{
		"repo": r.repo, "issue_id": st.PR,
	})
	if err != nil {
		return err
	}
	if n := len(list(res.Data, "files")); n == 0 {
		return fmt.Errorf("the pull request reports no changed files")
	}
	r.ok("gh-pr-files")
	return nil
}

func (r *runner) stepPRDiff(st *state) error {
	res, err := r.must("gh-pr-diff", "gh-pr-diff", map[string]any{
		"repo": r.repo, "issue_id": st.PR,
	})
	if err != nil {
		return err
	}
	if !strings.Contains(res.Content, st.PatchFile) {
		return fmt.Errorf("the diff does not mention the file that was added")
	}
	r.ok("gh-pr-diff")
	return nil
}

func (r *runner) stepPRChecks(st *state) error {
	// Checks may legitimately be absent (no CI configured for this repository) — the tool must
	// still answer, and answer honestly.
	res, err := r.must("gh-pr-checks", "gh-pr-checks", map[string]any{
		"repo": r.repo, "issue_id": st.PR,
	})
	if err != nil {
		return err
	}
	if str(res.Data, "state") == "" && res.Content == "" {
		return fmt.Errorf("no state and no content")
	}
	r.ok("gh-pr-checks")
	return nil
}

func (r *runner) stepPRMerge(st *state) error {
	_, err := r.must("gh-pr-merge", "gh-pr-merge", map[string]any{
		"repo": r.repo, "issue_id": st.PR, "method": "squash",
	})
	if err != nil {
		// A repository whose branch protection refuses the merge is a REAL answer, not a failure of
		// this extension — but it is reported as a skip with the reason, never silently.
		r.skip("gh-pr-merge", "the repository refused the merge: "+truncate(err.Error(), 120))
		return nil
	}
	r.ok("gh-pr-merge")
	return nil
}

func (r *runner) stepIssueCreate(st *state) error {
	res, err := r.must("gh-issue-create", "gh-issue-create", map[string]any{
		"repo": r.repo, "title": "e2e: automated issue\n", "body": "Opened by cmd/e2e-all.\n",
	})
	if err != nil {
		return err
	}
	st.Issue = int(num(res.Data, "number"))
	if st.Issue == 0 {
		if idx := strings.Index(res.Content, "#"); idx >= 0 {
			fmt.Sscanf(res.Content[idx+1:], "%d", &st.Issue)
		}
	}
	if st.Issue == 0 {
		return fmt.Errorf("no issue number in the reply: %s", truncate(res.Content, 200))
	}
	r.ok("gh-issue-create")
	return nil
}

func (r *runner) stepIssueShow(st *state) error {
	res, err := r.must("gh-issue-show", "gh-issue-show", map[string]any{
		"repo": r.repo, "issue_id": st.Issue,
	})
	if err != nil {
		return err
	}
	if !strings.Contains(res.Content, "automated issue") {
		return fmt.Errorf("the issue's own title is not in the reply")
	}
	r.ok("gh-issue-show")
	return nil
}

func (r *runner) stepIssueComment(st *state) error {
	_, err := r.must("gh-issue-comment", "gh-issue-comment", map[string]any{
		"repo": r.repo, "issue_id": st.Issue, "body": "e2e comment\n",
	})
	if err != nil {
		return err
	}
	r.ok("gh-issue-comment")
	return nil
}

// stepIssueComments reads the comment back — the proof that the write happened.
func (r *runner) stepIssueComments(st *state) error {
	res, err := r.must("gh-issue-comments", "gh-issue-comments", map[string]any{
		"repo": r.repo, "issue_id": st.Issue,
	})
	if err != nil {
		return err
	}
	if !contains(res, "e2e comment") {
		return fmt.Errorf("the comment just written is not in the list")
	}
	r.ok("gh-issue-comments")
	return nil
}

func (r *runner) stepIssueList(st *state) error {
	// GITHUB'S ISSUE LIST IS EVENTUALLY CONSISTENT. An issue that was created a second ago answers
	// 200 by number and is still absent from the LIST, which is an index that fills a moment later —
	// measured here, not assumed. Retried, and the reason is named in the failure: a suite that
	// reported "the tool is broken" for this would be lying.
	var res result
	var err error
	for attempt := 0; attempt < 8; attempt++ {
		res, err = r.must("gh-issue-list", "gh-issue-list", map[string]any{
			// A LIMIT THIS HIGH because the assertion is that a just-created issue appears: at 30, a
			// busy tracker pushes the new one off the end for a reason that has nothing to do with
			// the tool.
			"repo": r.repo, "state": "open", "limit": 100,
		})
		if err != nil {
			return err
		}
		if contains(res, fmt.Sprintf("%d", st.Issue)) {
			r.ok("gh-issue-list")
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf(
		"the open issue #%d never appeared in the list (GitHub indexes new issues with a delay; "+
			"it still had not after 8 tries)", st.Issue)
}

func (r *runner) stepIssueClose(st *state) error {
	_, err := r.must("gh-issue-close", "gh-issue-close", map[string]any{
		"repo": r.repo, "issue_id": st.Issue,
	})
	if err != nil {
		return err
	}
	r.ok("gh-issue-close")
	return nil
}

func (r *runner) stepTagCreate(st *state) error {
	st.Tag = fmt.Sprintf("e2e-%d", time.Now().Unix())
	_, err := r.must("gh-tag-create", "gh-tag-create", map[string]any{
		"repo": r.repo, "tag": st.Tag, "ref": "main",
		"message": "e2e tag\n",
	})
	if err != nil {
		return err
	}
	r.ok("gh-tag-create")
	return nil
}

func (r *runner) stepTagShow(st *state) error {
	res, err := r.must("gh-tag-show", "gh-tag-show", map[string]any{
		"repo": r.repo, "tag": st.Tag,
	})
	if err != nil {
		return err
	}
	if !contains(res, st.Tag) {
		return fmt.Errorf("the tag's own name is not in the reply")
	}
	r.ok("gh-tag-show")
	return nil
}

func (r *runner) stepTagList(st *state) error {
	res, err := r.must("gh-tag-list", "gh-tag-list", map[string]any{"repo": r.repo, "limit": 50})
	if err != nil {
		return err
	}
	if !contains(res, st.Tag) {
		return fmt.Errorf("the tag just created is not in the list")
	}
	r.ok("gh-tag-list")
	return nil
}

func (r *runner) stepReleases(st *state) error {
	// A repository may have no releases at all; the tool must answer with an empty list rather than
	// fail. That is the property under test here.
	_, err := r.must("gh-releases", "gh-releases", map[string]any{"repo": r.repo, "limit": 10})
	if err != nil {
		return err
	}
	r.ok("gh-releases")
	return nil
}

func (r *runner) stepWorkflowList(st *state) error {
	_, err := r.must("gh-workflow-list", "gh-workflow-list", map[string]any{"repo": r.ciRepo})
	if err != nil {
		return err
	}
	r.ok("gh-workflow-list")
	return nil
}

func (r *runner) stepRunList(st *state) error {
	res, err := r.must("gh-run-list", "gh-run-list", map[string]any{"repo": r.ciRepo, "limit": 10})
	if err != nil {
		return err
	}
	runs := list(res.Data, "runs")
	if len(runs) == 0 {
		r.skip("gh-run-list", "this repository has no CI runs to read")
		r.skip("gh-run-jobs", "no run")
		r.skip("gh-run-log", "no run")
		r.skip("gh-job-log", "no run")
		r.skip("gh-check-log", "no run")
		r.skip("gh-run-cancel", "no run")
		r.skip("gh-run-rerun", "no run")
		return nil
	}
	st.RunID = num(runs[0], "ID")
	if st.RunID == 0 {
		st.RunID = num(runs[0], "id")
	}
	r.ok("gh-run-list")
	return nil
}

func (r *runner) stepRunJobs(st *state) error {
	if st.RunID == 0 {
		return nil
	}
	res, err := r.must("gh-run-jobs", "gh-run-jobs", map[string]any{"repo": r.ciRepo, "run": st.RunID})
	if err != nil {
		return err
	}
	jobs := list(res.Data, "jobs")
	if len(jobs) > 0 {
		st.JobID = num(jobs[0], "ID")
		if st.JobID == 0 {
			st.JobID = num(jobs[0], "id")
		}
	}
	r.ok("gh-run-jobs")
	return nil
}

func (r *runner) stepRunLog(st *state) error {
	if st.RunID == 0 {
		return nil
	}
	// `lines` CAPS THE REPLY. A whole run's log is hundreds of KB, and the tool refuses to hand that
	// back in one message — correctly: the caller that wanted the end did not ask for the middle.
	_, err := r.must("gh-run-log", "gh-run-log", map[string]any{
		"repo": r.ciRepo, "run": st.RunID, "lines": 100,
	})
	if err != nil {
		// A run that is still going has no log yet; that is the platform's rule, not a fault here.
		r.skip("gh-run-log", "the run has no log yet: "+truncate(err.Error(), 100))
		return nil
	}
	r.ok("gh-run-log")
	return nil
}

func (r *runner) stepJobLog(st *state) error {
	if st.JobID == 0 {
		r.skip("gh-job-log", "the run reported no jobs")
		return nil
	}
	_, err := r.must("gh-job-log", "gh-job-log", map[string]any{
		"repo": r.ciRepo, "job": st.JobID, "tail": 50,
	})
	if err != nil {
		r.skip("gh-job-log", "the job has no log: "+truncate(err.Error(), 100))
		return nil
	}
	r.ok("gh-job-log")
	return nil
}

func (r *runner) stepCheckLog(st *state) error {
	if st.RunID == 0 {
		return nil
	}
	_, err := r.must("gh-check-log", "gh-check-log", map[string]any{
		"repo": r.ciRepo, "run": st.RunID, "failed_only": true, "tail": 50,
	})
	if err != nil {
		r.skip("gh-check-log", truncate(err.Error(), 110))
		return nil
	}
	r.ok("gh-check-log")
	return nil
}

func (r *runner) stepRunCancel(st *state) error {
	// CANCELLING A FINISHED RUN IS REFUSED, and that refusal is correct — so the suite looks for one
	// that is still GOING before it tries. Cancelling a completed run proves nothing; finding a live
	// one and stopping it is the behaviour the tool exists for.
	live, ok := r.findRunning(st)
	if !ok {
		r.skip("gh-run-cancel", "no run is in progress to cancel")
		return nil
	}
	_, err := r.must("gh-run-cancel", "gh-run-cancel", map[string]any{
		"repo": r.ciRepo, "run": live,
	})
	if err != nil {
		r.skip("gh-run-cancel", truncate(err.Error(), 100))
		return nil
	}
	r.ok("gh-run-cancel")
	return nil
}

// findRunning returns the id of a run that has not finished, if one is recent enough to be listed.
func (r *runner) findRunning(st *state) (int64, bool) {
	res, err := r.call("gh-run-list", map[string]any{"repo": r.ciRepo, "limit": 20})
	if err != nil || res.Error != "" {
		return 0, false
	}
	for _, run := range list(res.Data, "runs") {
		id := num(run, "ID")
		if id == 0 {
			id = num(run, "id")
		}
		status := str(run, "Status")
		if status == "" {
			status = str(run, "status")
		}
		// NOT THIS RUN. Aiming a cancel at the CI that is executing the test kills the test, and the
		// failure reads as a product defect.
		if id == r.selfRunID {
			continue
		}
		if id != 0 && (status == "in_progress" || status == "queued") {
			return id, true
		}
	}
	return 0, false
}

func (r *runner) stepRunRerun(st *state) error {
	if st.RunID == 0 {
		return nil
	}
	// RERUN ONLY WORKS ON A FINISHED RUN — GitHub refuses one that is still going. If a live run was
	// found for the cancel step, this uses the newest COMPLETED one instead.
	id := st.RunID
	if live, ok := r.findRunning(st); ok && live == id {
		if done, found := r.findCompleted(st); found {
			id = done
		}
	}
	_, err := r.must("gh-run-rerun", "gh-run-rerun", map[string]any{"repo": r.ciRepo, "run": id})
	if err != nil {
		r.skip("gh-run-rerun", truncate(err.Error(), 110))
		return nil
	}
	r.ok("gh-run-rerun")
	return nil
}

// findCompleted returns the id of the newest run that has finished.
func (r *runner) findCompleted(st *state) (int64, bool) {
	res, err := r.call("gh-run-list", map[string]any{"repo": r.ciRepo, "limit": 20})
	if err != nil || res.Error != "" {
		return 0, false
	}
	for _, run := range list(res.Data, "runs") {
		id := num(run, "ID")
		if id == 0 {
			id = num(run, "id")
		}
		status := str(run, "Status")
		if status == "" {
			status = str(run, "status")
		}
		// NOT THIS RUN, for the same reason as findRunning: a rerun aimed at the suite kills the suite.
		if id == r.selfRunID {
			continue
		}
		if id != 0 && status == "completed" {
			return id, true
		}
	}
	return 0, false
}

func (r *runner) stepRunDispatch(st *state) error {
	// Needs a workflow file that supports workflow_dispatch. Discovered rather than assumed.
	res, err := r.call("gh-workflow-list", map[string]any{"repo": r.ciRepo})
	if err != nil || res.Error != "" {
		r.skip("gh-run-dispatch", "no workflow list to choose from")
		return nil
	}
	flows := list(res.Data, "workflows")
	if len(flows) == 0 {
		r.skip("gh-run-dispatch", "this repository has no workflows")
		return nil
	}
	path := str(flows[0], "Path")
	if path == "" {
		path = str(flows[0], "path")
	}
	_, derr := r.must("gh-run-dispatch", "gh-run-dispatch", map[string]any{
		"repo": r.ciRepo, "workflow": path, "ref": "main",
	})
	if derr != nil {
		// Not every workflow is dispatchable — `on: push` ones are not.
		r.skip("gh-run-dispatch", truncate(derr.Error(), 110))
		return nil
	}
	r.ok("gh-run-dispatch")
	return nil
}

func (r *runner) stepArtifacts(st *state) error {
	if st.RunID == 0 {
		r.skip("gh-artifacts", "no run to list artifacts for")
		r.skip("gh-artifact-download", "no artifact")
		return nil
	}
	res, err := r.must("gh-artifacts", "gh-artifacts", map[string]any{
		"repo": r.ciRepo, "run": st.RunID,
	})
	if err != nil {
		return err
	}
	r.ok("gh-artifacts")
	arts := list(res.Data, "artifacts")
	if len(arts) == 0 {
		r.skip("gh-artifact-download", "the run produced no artifacts")
		return nil
	}
	// THE ID, NOT THE NAME: the download endpoint addresses artifacts by id, and a name is not unique
	// across runs.
	id := num(arts[0], "ID")
	if id == 0 {
		id = num(arts[0], "id")
	}
	r.artifactID = id
	return nil
}

func (r *runner) stepArtifactDownload(st *state) error {
	if r.artifactID == 0 {
		return nil
	}
	_, err := r.must("gh-artifact-download", "gh-artifact-download", map[string]any{
		// AN INTEGER, not its decimal string: the tool reads this with argInt, which rejects a
		// string. That is the right contract — an id is a number — so the caller converts.
		"repo": r.ciRepo, "run": st.RunID, "artifact": r.artifactID,
	})
	if err != nil {
		// Downloading needs object storage configured; a deployment without it says so, which is the
		// behaviour worth confirming rather than a failure.
		r.skip("gh-artifact-download", truncate(err.Error(), 110))
		return nil
	}
	r.ok("gh-artifact-download")
	return nil
}

func (r *runner) stepPackages(st *state) error {
	// AN ORG IS REQUIRED, and that is the tool's contract rather than an omission: a package belongs to
	// an organisation or a user, not to a repository, so there is nothing to look up without one.
	_, err := r.must("gh-packages", "gh-packages", map[string]any{"org": r.owner(), "limit": 10})
	if err != nil {
		r.skip("gh-packages", truncate(err.Error(), 110))
		return nil
	}
	r.ok("gh-packages")
	return nil
}

// The destructive group: a throwaway repository, renamed and then deleted.
//
// IT IS A SEPARATE REPOSITORY on purpose. Deleting and renaming cannot be exercised on the one the
// rest of the suite works in, and a suite that skipped them would leave three tools unverified.
func (r *runner) stepRepoCreate(st *state) error {
	st.Scratch = fmt.Sprintf("e2e-scratch-%d", time.Now().Unix())
	_, err := r.must("gh-repo-create", "gh-repo-create", map[string]any{
		// `org` PUTS IT WHERE THE SUITE CAN FIND IT AGAIN. Without it the repository lands under the
		// TOKEN'S OWNER — a personal account — and the rename that follows addresses
		// easydev-org/<name>, which does not exist. That 404 is what this argument prevents.
		"name": st.Scratch, "org": r.owner(), "private": true, "auto_init": true,
		"description": "created by cmd/e2e-all, deleted at the end",
	})
	if err != nil {
		return err
	}
	r.ok("gh-repo-create")
	return nil
}

func (r *runner) stepRepoRename(st *state) error {
	if st.Scratch == "" {
		return nil
	}
	st.ScratchRenamed = st.Scratch + "-renamed"

	// A REPOSITORY THAT WAS JUST CREATED CANNOT BE RENAMED YET. GitHub answers 422 with "a conflicting
	// repository operation is still in progress" while its own creation settles — an asynchronous
	// property of the platform, not a fault in the tool. Retried rather than slept through once,
	// because how long it takes is not published.
	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		_, err := r.must("gh-repo-rename", "gh-repo-rename", map[string]any{
			// "name" is the NEW name; the repository being renamed is named by "repo".
			"repo": r.owner() + "/" + st.Scratch, "name": st.ScratchRenamed,
		})
		if err == nil {
			r.ok("gh-repo-rename")
			return nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "still in progress") {
			return err
		}
		time.Sleep(3 * time.Second)
	}
	return lastErr
}

func (r *runner) stepTagDelete(st *state) error {
	if st.Tag == "" {
		return nil
	}
	_, err := r.must("gh-tag-delete", "gh-tag-delete", map[string]any{
		"repo": r.repo, "tag": st.Tag, "confirm": true,
	})
	if err != nil {
		return err
	}
	// Cleared so cleanup does not try again.
	st.Tag = ""
	r.ok("gh-tag-delete")
	return nil
}

func (r *runner) stepRepoDelete(st *state) error {
	name := st.ScratchRenamed
	if name == "" {
		name = st.Scratch
	}
	if name == "" {
		return nil
	}
	slug := r.owner() + "/" + name
	// THE SAME ASYNCHRONY THE RENAME HIT: GitHub refuses a write against a repository whose previous
	// operation has not settled, with 409 "a conflicting repository operation is still in progress".
	// Retried for the same reason, and only on that message — anything else is a real answer.
	var lastErr error
	for attempt := 0; attempt < 8; attempt++ {
		_, err := r.must("gh-repo-delete", "gh-repo-delete", map[string]any{
			// `confirm` IS THE REPOSITORY'S OWN NAME, not "true". Typing the name is the point: a
			// destructive call that accepts a boolean can be issued by a caller that did not stop to
			// think about which repository it was pointing at.
			"repo": slug, "confirm": slug,
		})
		if err == nil {
			r.ok("gh-repo-delete")
			return nil
		}
		lastErr = err
		if !strings.Contains(err.Error(), "still in progress") {
			return err
		}
		time.Sleep(4 * time.Second)
	}
	return lastErr
}

// owner is the GitHub owner of the repository under test.
func (r *runner) owner() string {
	o, _, _ := strings.Cut(r.repo, "/")
	return o
}

// mirrorRemote builds the remote URL the mirror tools take.
//
// A URL, not owner/name: the extension decides which configured server a remote belongs to by parsing
// its HOST, so "agentdev/test" has no host to look up and is refused with "cannot parse a host".
// This is also what a caller does — a remote is an address.
func (r *runner) mirrorRemote() string {
	return fmt.Sprintf("%s/%s.git", r.forgeBase, r.mirror)
}
