package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/ghclient"
	"github.com/easydev-org/easybus-sdk/job"
)

/**
 * CIKind is this extension's word for the jobs it runs.
 *
 * A RUN IS NOT A PROCESS. Nothing here owns it; GitHub does. What this extension owns is the
 * knowledge that somebody is waiting for it, and that is what the shared list records.
 */
const CIKind = "ci-run"

// runPollEvery is how often a tracked run is re-read.
//
// FIFTEEN SECONDS, WHICH IS GITHUB'S OWN LATENCY. A workflow's status does not change faster than
// that in practice, and polling a rate-limited API per second would spend the budget to learn
// nothing. The cost of the interval is that a finished run is noticed up to fifteen seconds late,
// which nobody watching a build minds.
const runPollEvery = 15 * time.Second

/**
 * trackRun registers a workflow run and follows it to its end.
 *
 * IT RETURNS IMMEDIATELY. The run belongs to GitHub; the call that asked for it is over.
 *
 * THE FOLLOW IS A POLL, AND IT HAS TO BE. A sandbox job can be waited on because the worker offers a
 * blocking JobWait over its own connection; GitHub offers a status read and nothing else, so the
 * only way to learn that a run ended is to ask again. The ticker is the difference between this
 * extension and the sandbox's, and it is caused by who owns the work.
 */
func (t *toolset) trackRun(ctx context.Context, c *ghclient.Client, repo string, runID int64, title, session, tenant string) {
	if t.deps.Jobs == nil {
		// NO LIST, NO BACKGROUND JOB: the tool already returned its "started" reply, and a job
		// nobody can look up is worse than one that was never registered.
		return
	}

	id := CIKind + "." + strconv.FormatInt(runID, 10)
	handle, err := t.deps.Jobs.Start(ctx, job.Entry{
		ID:      id,
		Kind:    CIKind,
		Title:   title,
		ExtID:   "gitdev",
		Tenant:  tenant,
		Session: session,
		/**
		 * REQUEST, NOT KILL. Cancelling a GitHub run sets a flag; the step in progress finishes
		 * before the run stops, and GitHub may decline. A caller told `kill` would believe the
		 * machine had stopped when what exists is a request in flight.
		 */
		CancelMode: job.CancelRequest,
	})
	if err != nil {
		slog.WarnContext(ctx, "gitdev: cannot register run", "svc", "gitdev-ext", "run", runID, "err", err)
		return
	}

	// THE HEARTBEAT OUTLIVES THE CALL, which ends as soon as this returns.
	bg := context.WithoutCancel(ctx)
	go handle.KeepAlive(bg)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("gitdev: run follower panicked", "svc", "gitdev-ext", "run", runID, "panic", r)
			}
		}()
		ticker := time.NewTicker(runPollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-bg.Done():
				return
			case <-ticker.C:
			}
			run, err := c.Run(bg, runID)
			if err != nil {
				// ONE FAILED POLL IS NOT THE END OF THE RUN. GitHub 5xx and rate limits are
				// transient, and recording an ending because a request failed would report a
				// result GitHub never gave.
				slog.WarnContext(bg, "gitdev: cannot read run", "svc", "gitdev-ext", "run", runID, "err", err)
				continue
			}
			if run.Status != "completed" {
				continue
			}
			state, exit := runOutcome(run.Conclusion)
			if err := handle.Finish(bg, state, exit, conclusionReason(run.Conclusion)); err != nil {
				slog.WarnContext(bg, "gitdev: cannot record run end", "svc", "gitdev-ext", "run", runID, "err", err)
			}
			if t.deps.NotifyJobDone != nil {
				if err := t.deps.NotifyJobDone(bg, tenant, session, id, title, state, exit); err != nil {
					slog.WarnContext(bg, "gitdev: cannot wake the conversation", "svc", "gitdev-ext", "run", runID, "err", err)
				}
			}
			return
		}
	}()
}

/**
 * runOutcome maps a GitHub conclusion onto our state and an exit code.
 *
 * `failure` BECOMES `done` WITH EXIT 1, not a `failed` state: we settled that states say why a job
 * stopped and exit codes say what the ending was worth. GitHub's conclusion is the second thing, so
 * it travels as the code.
 *
 * THE CODES ARE CONVENTIONAL, NOT INVENTED HERE: 0 success, 1 failure, 124 timeout (the
 * coreutils convention) — anything reading them already knows those numbers. `cancelled` and
 * `skipped` carry no code, because neither is a process that exited.
 */
func runOutcome(conclusion string) (job.State, *int) {
	code := func(n int) *int { return &n }
	switch conclusion {
	case "success":
		return job.StateDone, code(0)
	case "failure":
		return job.StateDone, code(1)
	case "timed_out":
		return job.StateDone, code(124)
	case "cancelled":
		return job.StateCancelled, nil
	case "skipped":
		return job.StateDone, code(0)
	case "action_required":
		// NOT A FAILURE OF THE WORK: it is waiting on a human. Recorded as an ending because
		// nothing here will change without somebody acting, and as `done` with no code because the
		// jobs did not run.
		return job.StateDone, nil
	default:
		return job.StateDone, nil
	}
}

// conclusionReason turns a conclusion into the sentence a reader sees.
func conclusionReason(conclusion string) string {
	switch conclusion {
	case "success":
		return ""
	case "failure":
		return "one or more jobs failed"
	case "timed_out":
		return "the run exceeded its time limit"
	case "cancelled":
		return "the run was cancelled"
	case "skipped":
		return "the run was skipped"
	case "action_required":
		return "the run is waiting for a person to approve it"
	default:
		return fmt.Sprintf("finished with conclusion %q", conclusion)
	}
}
