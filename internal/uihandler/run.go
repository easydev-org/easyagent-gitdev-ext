package uihandler

import "context"

/**
 * RunSource reads CI runs from GitHub for the UI's run pages.
 *
 * WHY A PAGE AND NOT ONLY A CARD: a run is the one thing here whose state CHANGES while a reader
 * watches it — steps turn green one by one — and a card is a snapshot from the moment the tool ran.
 * A page can be left open and refreshed.
 *
 * THE LOGS ARE NOT PART OF THIS. A run's logs are megabytes and a run has several jobs, so fetching
 * them with the run would make opening the page cost the whole log whether or not anybody read it.
 * They are a separate call per job, made when the reader asks.
 */
type RunSource interface {
	// Runs lists recent runs for a repository, newest first.
	Runs(ctx context.Context, repo string, limit int) ([]RunView, error)
	// Run returns one run's own facts.
	Run(ctx context.Context, repo string, id int64) (RunView, error)
	// Jobs returns where a run has got to: its jobs and each job's steps.
	Jobs(ctx context.Context, repo string, id int64) ([]JobView, error)
	// JobLog returns one job's log, bounded so a huge log cannot be handed to a browser whole.
	JobLog(ctx context.Context, repo string, jobID int64) (string, error)
}

// RunView is one workflow run as the page shows it.
type RunView struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued | in_progress | completed
	Conclusion string `json:"conclusion"` // success | failure | … (empty while running)
	Branch     string `json:"branch"`
	Event      string `json:"event"`
	HeadSHA    string `json:"headSha"`
	URL        string `json:"url"`
	CreatedAt  string `json:"createdAt"`
	Attempt    int    `json:"attempt"`
	// Done is computed rather than left to the UI: three places would otherwise each have to know
	// that "completed with no conclusion" is a state that cannot happen but must not be guessed at.
	Done bool `json:"done"`
}

// JobView is one job of a run, with its steps.
type JobView struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Status     string     `json:"status"`
	Conclusion string     `json:"conclusion"`
	StartedAt  string     `json:"startedAt"`
	URL        string     `json:"url"`
	Steps      []StepView `json:"steps"`
	// LogAvailable says whether the log can be fetched at all: GitHub publishes a job's log only
	// once the job is finished, so a running job has nothing to show and a button for it would fail.
	LogAvailable bool `json:"logAvailable"`
}

// StepView is one step inside a job.
type StepView struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	Number     int    `json:"number"`
}
