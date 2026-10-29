// Package tools implements the gh-* tool handlers.
//
// One rule shapes this package: the ONLY way to change a repository is gh-pr-create, which
// turns the staging area into a branch and a pull request. There is no commit tool, no push
// tool, and no tool that writes to a branch. A caller cannot accidentally take a shortcut
// because there is no shortcut to take.
package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/config"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/i18n"
)

// easybusInternal is the SDK's internal-error code, aliased so a handler can say "the
// deployment is misconfigured" without importing the SDK for one constant.
const easybusInternal = easybus.ToolResultErrorCodeInternal

// Deps is what the tools need from the extension host.
type Deps struct {
	GetConfig func(name, session, tenant string) any
	Locale    func(session, tenant string) string
	// PutObject stores a large object for a tenant. Used by gh-artifact-download: an artifact is
	// a zip of arbitrary size, so it is stored and referenced by name rather than returned in the
	// tool result. Nil means the deployment has no object storage, and that tool reports so.
	PutObject func(ctx context.Context, tenant, name string, data []byte) error
	// ReportProgress publishes one in-flight update for a call. Nil disables progress: handlers
	// keep working, they just say nothing until the result.
	ReportProgress func(ctx context.Context, tenant, callID string, p *easybus.ToolProgress) error
}

// Handler is one tool implementation.
type Handler func(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error)

// Permission keys this extension declares.
//
// TWO KEYS. Reading, opening an issue and commenting need none of them; those do not change
// anything, and gating a comment would only stop a session from asking a question.
//
//	repo.maintainer  every operation that CHANGES something: close an issue, open or merge a pull
//	                 request, run CI, tag a release, rename or delete the repository
//	org.admin        create a repository — organization level, because the repository does not
//	                 exist yet and there is nothing to scope a grant to
//
// There is deliberately no "developer" level. It existed to separate proposing from deciding, and
// every operation had to be argued into one side: is opening a pull request proposing? is running
// CI? is closing an issue? The answers were never convincing, and the ownership rule that came
// with it was always true in this deployment (one token, many sessions), so it protected nothing
// while looking as though it did.
const (
	// PermMaintainer may change the repository and everything in it.
	PermMaintainer = "repo.maintainer"
	// PermOrgAdmin creates repositories.
	PermOrgAdmin = "org.admin"
	// NOTE: there is deliberately no key for publishing a release. Releases are published by CI,
	// so no grant would make a hand-made one allowed.
)

// requireMap names the permissions each tool needs.
//
// Only WRITE-class tools appear here. Read tools are deliberately ungated: a session that may
// not merge should still be able to LOOK at the pull request, and gating reads would make every
// grant also a grant of visibility.
var requireMap = map[string][]string{
	// Everything that CHANGES something.
	"gh-issue-close":  {PermMaintainer},
	"gh-pr-create":    {PermMaintainer},
	"gh-pr-merge":     {PermMaintainer},
	"gh-run-dispatch": {PermMaintainer},
	"gh-run-rerun":    {PermMaintainer},
	"gh-run-cancel":   {PermMaintainer},
	"gh-tag-create":   {PermMaintainer},
	"gh-tag-delete":   {PermMaintainer},
	"gh-repo-rename":  {PermMaintainer},
	"gh-repo-delete":  {PermMaintainer},

	// Organization level: the repository does not exist yet.
	"gh-repo-create": {PermOrgAdmin},
}

// RequireMap returns a copy so a caller cannot mutate the shared table.
func RequireMap() map[string][]string {
	out := make(map[string][]string, len(requireMap))
	for k, v := range requireMap {
		out[k] = append([]string(nil), v...)
	}
	return out
}

// Registry builds the gh-* handler map.
func Registry(d Deps) map[string]Handler {
	t := &toolset{deps: d}
	raw := map[string]Handler{
		// THE MIRROR. A local, writable copy of the repository that changes are made in.
		//
		// GitHub is the AUTHORITY: what is merged, reviewed and released lives there. The mirror
		// exists so a change can be made without a clone — a branch is a ref, a file is an object,
		// and a commit is one call. `git-push-branch` is the only way out of it, and `gh-pr-create`
		// is the only way in.
		//
		// These live in this extension rather than their own because the workflow is one thing:
		// "change the code and land it". Splitting it meant an agent watching a patch succeed and a
		// push fail, with two sets of errors and two credentials to explain.
		"git-sync-upstream": t.syncUpstream,
		"git-branch-create": t.branchCreate,
		"git-file-patch":    t.filePatch,
		"git-file-read":     t.fileRead,
		"git-file-list":     t.fileList,
		"git-push-branch":   t.pushBranch,
		// The way BACK out: a branch pushed for review is a branch somebody has to be able to remove,
		// and until this existed the suite's own tidy-up had to go around the extension to do it.
		"git-branch-delete": t.branchDelete,

		// GITHUB PLATFORM. Repository metadata, account, releases, packages.
		"gh-info":     t.info,
		"gh-whoami":   t.whoami,
		"gh-orgs":     t.orgs,
		"gh-repos":    t.repos,
		"gh-releases": t.releases,
		"gh-packages": t.packages,

		// Publishing. gh-tag-create is the event a downstream subscription waits for.
		"gh-tag-create": t.tagCreate,
		"gh-tag-list":   t.tagList,
		"gh-tag-show":   t.tagShow,
		"gh-tag-delete": t.tagDelete,

		// Repository administration.
		"gh-repo-create": t.repoCreate,
		"gh-repo-rename": t.repoRename,
		"gh-repo-delete": t.repoDelete,

		// Pull-request-only actions. Everything else about a pull request — its title,
		// body, comments, closing — is an ISSUE operation, because GitHub models a pull
		// request as a kind of issue sharing one numbering space. See items.go.
		"gh-pr-create": t.prCreate,
		"gh-pr-checks": t.prChecks,
		"gh-pr-merge":  t.prMerge,

		// What a pull request CHANGED. Without these, reviewing a PR means reading its title.
		"gh-pr-files": t.prFiles,
		"gh-pr-diff":  t.prDiff,

		// CI. gh-run-jobs is the live view; gh-run-log is available once a job finishes.
		"gh-workflow-list":     t.workflowList,
		"gh-run-list":          t.runList,
		"gh-run-jobs":          t.runJobs,
		"gh-run-log":           t.runLog,
		"gh-job-log":           t.jobLog,
		"gh-check-log":         t.checkLog,
		"gh-artifacts":         t.artifactList,
		"gh-artifact-download": t.artifactDownload,
		"gh-run-rerun":         t.runRerun,
		"gh-run-cancel":        t.runCancel,
		"gh-run-dispatch":      t.runDispatch,

		// ONE set of tools for issues AND pull requests: one number, one object, so a caller
		// need not know in advance which it holds.
		"gh-issue-create":   t.issueCreate,
		"gh-issue-list":     t.issueList,
		"gh-issue-show":     t.issueShow,
		"gh-issue-comment":  t.issueComment,
		"gh-issue-comments": t.issueComments,
		"gh-issue-close":    t.issueClose,
	}

	// Every handler's result is normalised ON THE WAY OUT, at the one place they all pass through.
	//
	// The transport carries `Data` as a protobuf Struct, which holds scalars, lists and maps — not Go
	// structs. A struct in the payload does not fail loudly: the whole `data` field arrives as null,
	// so a client reading a field off it draws an empty card and nothing anywhere reports a problem.
	// Testing every tool is how this was found: gh-pr-diff returned data, gh-pr-files returned null,
	// and the only difference was a []FileChange.
	//
	// Done here rather than per handler so a NEW tool cannot forget it.
	out := make(map[string]Handler, len(raw))
	for name, h := range raw {
		out[name] = withNormalisedData(h)
	}
	return out
}

// toolset carries the dependencies through every handler.
type toolset struct {
	deps Deps
}

// requireRepo checks a permission against the repository a call targets.
//
// The repository is taken from the caller's arguments, not from configuration: what a session
// may do to `someone/elsewhere` is a different authorisation from what it may do here, and
// deriving it any other way would let a caller name one repository and be authorised for
// another.
func (t *toolset) requireRepo(ctx context.Context, perm string, repo string) error {
	if repo == "" {
		return &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: "a repository (owner/name) is required",
		}
	}
	target := map[string]string{"repo": repo}
	if extension.Allowed(ctx, perm, target) {
		return nil
	}
	// org.admin is scoped to an ORGANIZATION, not to a repository: creating "acme/new" asks about
	// the org named "acme". Checking it against {repo: ...} would never match, so a repository
	// creation would be impossible even with the grant.
	if perm == PermOrgAdmin {
		owner := repo
		if i := strings.IndexByte(repo, '/'); i > 0 {
			owner = repo[:i]
		}
		if extension.Allowed(ctx, PermOrgAdmin, map[string]string{"org": owner}) {
			return nil
		}
		return &extension.PermissionDenied{
			Key: PermOrgAdmin, Target: map[string]string{"org": owner},
			Granted: extension.GrantsFor(ctx, PermOrgAdmin),
		}
	}
	return &extension.PermissionDenied{Key: perm, Target: target, Granted: extension.GrantsFor(ctx, perm)}
}

// errArg builds a consistent invalid-argument error.
func errArg(msg string) error {
	return &extension.TypedError{Code: easybus.ToolResultErrorCodeInvalidArgument, Message: msg}
}

// errFailed builds a consistent failure error.
func errFailed(format string, a ...any) error {
	return &extension.TypedError{Code: easybus.ToolResultErrorCodeRetryable, Message: fmt.Sprintf(format, a...)}
}

// tr looks up a message for the caller's locale.
func (t *toolset) tr(session, tenant, key string, params map[string]any) string {
	loc := "en"
	if t.deps.Locale != nil {
		loc = t.deps.Locale(session, tenant)
	}
	return i18n.T(loc, key, params)
}

var _ = config.KnobToken
