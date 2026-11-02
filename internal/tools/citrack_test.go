package tools

import (
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
