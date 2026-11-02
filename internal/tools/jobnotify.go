package tools

import (
	"fmt"

	"github.com/easydev-org/easybus-sdk/job"
)

/**
 * JobText renders the message a finished run leaves in the conversation.
 *
 * IT NAMES THE RUN, ITS STATE AND ITS EXIT CODE, AND NOTHING ELSE. The log is deliberately absent: it
 * cannot even be fetched until a run is over, it is megabytes when it can, and `gh-run-log` reads it
 * on demand. Copying it here would put the same bytes in front of the model twice and would mean
 * writing a second truncation policy beside the one the agent applies to tool results.
 *
 * THE SHAPE IS THE SANDBOX EXTENSION'S, because a reader that has seen one finished job should
 * recognise the next — a wake and a `job-wait` return are read the same way regardless of who ran the
 * work.
 */
func JobText(jobID, title string, state job.State, exit *int) string {
	exitAttr := ""
	if exit != nil {
		exitAttr = fmt.Sprintf(" exit=%q", fmt.Sprintf("%d", *exit))
	}
	var how string
	switch state {
	case job.StateDone:
		// THE EXIT CODE CARRIES THE VERDICT, so this sentence does not. `failure` is `done` with
		// code 1 — see runOutcome — and saying "the run failed" here would repeat a judgement the
		// job deliberately leaves to the code.
		how = "The run finished."
	case job.StateCancelled:
		how = "The run was cancelled."
	case job.StateLost:
		// NOT PRODUCED HERE — a sweeper writes it after this process stops renewing. Rendered anyway
		// so an extension that comes back to its own job can describe it.
		how = "The run stopped being reported; its result is unknown."
	default:
		how = "The run reported a state this reader does not recognise."
	}
	return fmt.Sprintf(
		"<job id=%q state=%q%s title=%q>\n%s\nUse gh-run-log with run %s to read what it produced.\n</job>",
		jobID, string(state), exitAttr, title, how, runIDOf(jobID),
	)
}

// runIDOf strips the kind prefix off a job id, so the hint names the argument gh-run-log wants.
//
// A CALLER GIVEN A TASK-ID WOULD HAVE TO TRANSLATE IT. The job id is "ci-run.<run id>" and the tool
// takes the number; spelling that out here is the difference between a hint and a puzzle.
func runIDOf(jobID string) string {
	const prefix = CIKind + "."
	if len(jobID) > len(prefix) && jobID[:len(prefix)] == prefix {
		return jobID[len(prefix):]
	}
	return jobID
}

/**
 * MailboxPayload is what a wake carries.
 *
 * TWO FIELDS, AND THE SPLIT IS WHAT THE CONSUMER NEEDS. The agent folds this into a user message:
 * `text` becomes its body and `messageId` decides whether that has happened before. A wake that
 * arrives twice with the same id is folded once — which matters because the durable mailbox can
 * deliver twice, and a conversation showing the same completion twice is confusing in a way a missing
 * one is not.
 */
type MailboxPayload struct {
	MessageID string `json:"messageId"`
	Text      string `json:"text"`
}

// NewMailboxPayload builds the wake for one finished run.
//
// THE MESSAGE ID IS DERIVED FROM THE JOB, not generated: a run has one ending, so a wake about it
// should fold at most once however many times it is delivered.
func NewMailboxPayload(jobID, title string, state job.State, exit *int) MailboxPayload {
	return MailboxPayload{
		MessageID: "job-done-" + jobID,
		Text:      JobText(jobID, title, state, exit),
	}
}
