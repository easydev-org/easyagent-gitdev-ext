package uihandler

import (
	"context"
)

// TenantFromContext is what a PullSource uses to learn which tenant's token to read.
//
// Exported so the wiring in main can pass it through: the handler puts the tenant on the request
// context after authenticating, and the source reads it from there rather than authenticating a
// second time or trusting a query parameter.
var TenantFromContext = tenantFrom

// PullSource reads a pull request from GitHub for the UI's PR pages.
//
// WHY THIS EXISTS AT ALL: the rest of this package reads the LOCAL checkout, deliberately — a
// workspace is on disk, so the file tree, diffs and logs cost nothing and are already rate-limit
// free. A pull request is not on disk. It lives on GitHub, and there is no local copy to read.
//
// It is also the ONE place the UI queries the remote, and that is on purpose: a card renders the
// result the tool already returned, while a PAGE is somewhere the reader goes to see the current
// state. Somebody may have pushed since the tool ran; the page should say so rather than repeat a
// snapshot.
//
// Declared as an interface so this package does not depend on the GitHub client: the adapter lives
// with the client, and a deployment without a token simply leaves this nil.
type PullSource interface {
	// Pull returns the pull request's own facts.
	Pull(ctx context.Context, repo string, number int) (PullView, error)
	// Files returns what it changes, with per-file counts and patches.
	Files(ctx context.Context, repo string, number int) ([]FileView, error)
	// Diff returns the whole change as one unified diff.
	Diff(ctx context.Context, repo string, number int) (string, error)
	// Checks returns CI status.
	Checks(ctx context.Context, repo string, number int) (ChecksView, error)
}

// PullView is the pull request as the page shows it.
type PullView struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Author  string `json:"author"`
	Body    string `json:"body"`
	Head    string `json:"head"`
	Base    string `json:"base"`
	URL     string `json:"url"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	Created string `json:"created"`
}

// FileView is one file in the change.
type FileView struct {
	Path      string `json:"path"`
	Status    string `json:"status"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
	// Patch is the per-file diff. Empty is meaningful: PatchTruncated says whether that is because
	// GitHub omitted it (binary, or too large) rather than because nothing changed.
	Patch          string `json:"patch"`
	PatchTruncated bool   `json:"patch_truncated"`
}

// ChecksView is the CI status of a pull request.
type ChecksView struct {
	State   string     `json:"state"`
	HeadSHA string     `json:"sha"`
	Checks  []CheckRow `json:"checks"`
}

// CheckRow is one named check.
type CheckRow struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
