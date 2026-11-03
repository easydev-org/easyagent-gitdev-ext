package tools

import (
	"strings"
	"testing"
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
		state      string
		exit       *int
	}{
		// A GREEN BUILD AND A RED ONE ARE THE SAME STATE AND DIFFERENT CODES. Anything reading this
		// has to look at the code, which is where the answer lives.
		{"success", "done", intp(0)},
		{"failure", "done", intp(1)},
		{"timed_out", "done", intp(124)},
		// CANCELLED IS ITS OWN STATE, because the work did not finish and no process exited.
		{"cancelled", "cancelled", nil},
		{"skipped", "done", intp(0)},
		// WAITING ON A PERSON: the run did not fail, and no job produced a code.
		{"action_required", "done", nil},
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
	if state != "done" {
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
	got := JobText("ci-run.1234", "build on main", "done", intp(1), "")
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
	got := JobText("ci-run.7", "build", "cancelled", nil, "")
	if strings.Contains(got, "exit=") {
		t.Errorf("a run with no exit code rendered one:\n%s", got)
	}
}

// THE MESSAGE ID IS DERIVED, so a redelivered wake folds once rather than twice.
//
// AND THE PAYLOAD IS A MAP, because `PublishMailboxEvent` runs it through structpb, which cannot take
// a Go struct — the conversion fails, the SDK folds it to null, and the wake arrives with no body.
func TestMailboxPayloadIdIsDerivedFromTheRun(t *testing.T) {
	a := NewMailboxPayload("ci-run.1234", "build", "done", intp(0), "")
	b := NewMailboxPayload("ci-run.1234", "build", "done", intp(0), "")
	id, _ := a["message_id"].(string)
	if id == "" || id != b["message_id"] {
		t.Fatalf("the same run produced two ids: %q and %q", id, b["message_id"])
	}
	text, _ := a["text"].(string)
	if text == "" {
		t.Fatal("a wake with no text folds an empty message into the conversation")
	}
}

/**
 * THE CONCLUSION SURVIVES INTO THE WAKE, because nothing else carries it any more.
 *
 * `timed_out`, `action_required` and `skipped` are all `done` — a state says why work stopped, not
 * what the ending was worth. That difference used to travel in a `Reason` field on the shared
 * job-list entry; the entry is gone, so the wake is the only place it can live. Losing it here would
 * turn "waiting for a person to approve it" into the same message as "the build went green".
 */
func TestTheWakeCarriesTheConclusion(t *testing.T) {
	for _, tc := range []struct{ conclusion, want string }{
		{"timed_out", "the run exceeded its time limit"},
		{"action_required", "waiting for a person"},
		{"failure", "one or more jobs failed"},
		{"cancelled", "the run was cancelled"},
	} {
		state, exit := runOutcome(tc.conclusion)
		text, _ := NewMailboxPayload("ci-run.9", "build", state, exit,
			conclusionReason(tc.conclusion))["text"].(string)
		if text == "" {
			t.Fatalf("%s: the wake has no text", tc.conclusion)
		}
		if !strings.Contains(text, tc.want) {
			t.Errorf("%s: the wake does not say %q — GitHub's distinction was lost:\n%s",
				tc.conclusion, tc.want, text)
		}
	}
}
