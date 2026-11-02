package tools

import (
	"strings"
	"testing"

	"github.com/easydev-org/easybus-sdk/job"
)

/**
 * GITHUB'S CONCLUSION IS AN EXIT CODE, NOT A STATE.
 *
 * We settled that a state says WHY a job stopped and an exit code says what the ending was worth. A
 * `failure` is `done` with code 1 — not a `failed` state — because the job stopped normally and the
 * conclusion is the judgement.
 */
func TestRunOutcomeMapsConclusions(t *testing.T) {
	for _, tc := range []struct {
		conclusion string
		state      job.State
		exit       *int
	}{
		// A GREEN BUILD AND A RED ONE ARE THE SAME STATE AND DIFFERENT CODES. Anything reading this
		// has to look at the code, which is where the answer lives.
		{"success", job.StateDone, intp(0)},
		{"failure", job.StateDone, intp(1)},
		{"timed_out", job.StateDone, intp(124)},
		// CANCELLED IS ITS OWN STATE, because the work did not finish and no process exited.
		{"cancelled", job.StateCancelled, nil},
		{"skipped", job.StateDone, intp(0)},
		// WAITING ON A PERSON: the run did not fail, and no job produced a code.
		{"action_required", job.StateDone, nil},
	} {
		state, exit := runOutcome(tc.conclusion)
		if state != tc.state {
			t.Errorf("%s: state = %q, want %q", tc.conclusion, state, tc.state)
		}
		switch {
		case tc.exit == nil && exit != nil:
			t.Errorf("%s: exit = %d, want none", tc.conclusion, *exit)
		case tc.exit != nil && exit == nil:
			t.Errorf("%s: exit = none, want %d", tc.conclusion, *tc.exit)
		case tc.exit != nil && exit != nil && *tc.exit != *exit:
			t.Errorf("%s: exit = %d, want %d", tc.conclusion, *exit, *tc.exit)
		}
	}
}

/**
 * AN UNKNOWN CONCLUSION MUST NOT INVENT A SUCCESS.
 *
 * GitHub adds conclusions. If one arrives that this code has never seen, calling it a success with
 * exit 0 would report a green build for something nobody has assessed — so it is `done` with NO
 * exit code, which reads as "it ended and we do not know how".
 */
func TestAnUnknownConclusionCarriesNoExitCode(t *testing.T) {
	state, exit := runOutcome("something_github_added")
	if state != job.StateDone {
		t.Fatalf("state = %q, want done", state)
	}
	if exit != nil {
		t.Fatalf("an unrecognised conclusion was given exit %d; it should have none", *exit)
	}
	// AND IT SAYS SO, rather than sending the reader to a documentation page.
	if reason := conclusionReason("something_github_added"); reason == "" {
		t.Fatal("an unrecognised conclusion must be described, not silently treated as success")
	}
}

// A SUCCESSFUL RUN NEEDS NO EXPLANATION, and an empty reason keeps the message short.
func TestSuccessNeedsNoReason(t *testing.T) {
	if conclusionReason("success") != "" {
		t.Fatal("a successful run should not be explained")
	}
}

func intp(v int) *int { return &v }

/**
 * THE WAKE POINTS AT THE TOOL THAT READS THE LOG, WITH THE ARGUMENT IT WANTS.
 *
 * The job id is "ci-run.1234" and `gh-run-log` takes `run=1234`. A hint that repeated the job id would
 * send the caller to a tool with an argument that does not fit it — a puzzle rather than a pointer.
 */
func TestJobTextNamesTheRunAndPointsAtTheLog(t *testing.T) {
	got := JobText("ci-run.1234", "build on main", job.StateDone, intp(1))
	for _, want := range []string{"ci-run.1234", `state="done"`, `exit="1"`, `title="build on main"`, "gh-run-log", "run 1234"} {
		if !strings.Contains(got, want) {
			t.Errorf("the wake is missing %q:\n%s", want, got)
		}
	}
	// AND IT MUST NOT CONTRADICT THE EXIT CODE by calling a non-zero one a failure in prose: the
	// code carries the verdict.
	if strings.Contains(strings.ToLower(got), "failed") {
		t.Errorf("a done run must not be described as failed:\n%s", got)
	}
}

func TestJobTextOmitsAnAbsentExitCode(t *testing.T) {
	// A CANCELLED RUN HAS NO EXIT CODE, and rendering "exit 0" would invent a success.
	got := JobText("ci-run.7", "build", job.StateCancelled, nil)
	if strings.Contains(got, "exit=") {
		t.Errorf("a run with no exit code rendered one:\n%s", got)
	}
}

// THE MESSAGE ID IS DERIVED, so a redelivered wake folds once rather than twice.
func TestMailboxPayloadIdIsDerivedFromTheRun(t *testing.T) {
	a := NewMailboxPayload("ci-run.1234", "build", job.StateDone, intp(0))
	b := NewMailboxPayload("ci-run.1234", "build", job.StateDone, intp(0))
	if a.MessageID != b.MessageID {
		t.Fatalf("the same run produced two ids: %q and %q", a.MessageID, b.MessageID)
	}
	if a.Text == "" {
		t.Fatal("a wake with no text folds an empty message into the conversation")
	}
}
