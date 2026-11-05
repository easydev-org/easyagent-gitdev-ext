package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/ghclient"
)

// Reviewing pull requests. These are reads except merge and close, which are gated.

func (t *toolset) prChecks(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "issue_id")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"number" is required`)
	}
	ch, err := c.PRChecks(ctx, n)
	if err != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, err)
	}

	state := ch.State()
	var b strings.Builder
	fmt.Fprintf(&b, "CI: %s\n", state)

	// MERGEABILITY travels with CI because they answer the same question — "can this land?" — and a
	// caller that learns about a conflict only when `gh-pr-manage` fails has already spent the review.
	//
	// A failure to READ it is not a failure of the tool: CI status is still useful, and the merge
	// state is reported as unknown rather than dragging the whole result down.
	mergeable := "unknown"
	if pr, perr := c.PR(ctx, n); perr == nil {
		switch {
		case pr.Merged:
			mergeable = "merged"
		case pr.Mergeable == nil:
			// GitHub computes mergeability lazily; a request made right after a push answers null
			// and settles within seconds. Saying so is more useful than "false".
			mergeable = "computing"
		case *pr.Mergeable:
			mergeable = "clean"
		default:
			mergeable = "conflict"
		}
	}
	fmt.Fprintf(&b, "mergeable: %s\n", mergeable)
	if mergeable == "conflict" {
		b.WriteString("(the branch conflicts with its base — start a NEW branch from " +
			"`upstream:<base>` and redo the change; do not rewrite the branch under review)\n")
	}
	if state == "none" {
		b.WriteString("(no checks and no status contexts have reported for this commit)\n")
	}
	for _, r := range ch.Runs {
		fmt.Fprintf(&b, "  %-30s %s/%s\n", r.Name, r.Status, r.Conclusion)
	}
	for _, s := range ch.Statuses {
		fmt.Fprintf(&b, "  %-30s status: %s\n", s.Context, s.State)
	}
	return extension.ToolResultData{
		Content: b.String(),
		Data: map[string]any{
			"state": state, "checks": ch, "sha": ch.HeadSHA, "mergeable": mergeable,
			// CI status is a list of named checks — the shape a card is good at, and the thing a
			// reader wants at a glance before deciding whether to merge.
			"card": cardFor(cardTemplatePRCheck),
			"view": pullView(repo, n, fmt.Sprintf("%s#%d", repo, n)),
		},
	}, nil
}

/**
 * prClose closes a pull request the caller OPENED.
 *
 * IT REFUSES SOMEONE ELSE'S. Closing your own abandons your own proposal; closing another person's
 * discards work they are relying on. Those are the same verb and different acts, so they are different
 * tools with different effects — and the refusal is what makes the split real rather than advisory.
 *
 * THE OWNERSHIP CHECK IS SERVER-SIDE because the caller cannot be trusted to know: a session working from
 * a shared token may believe it opened a pull request that it did not, and "I thought it was mine" is not
 * a reason to discard someone's work.
 */
func (t *toolset) prClose(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	repo, err := required(args, "repo")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := t.requireRepo(ctx, PermWrite, repo); err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "pull_number")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"pull_number" is required`)
	}
	c, _, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}

	id, _, err := c.IdentityAndScopes(ctx)
	if err != nil {
		return extension.ToolResultData{}, errFailed("identify the token: %v", err)
	}
	pr, err := c.PR(ctx, n)
	if err != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, err)
	}
	if !strings.EqualFold(pr.Author, id.Login) {
		// A REFUSAL THAT NAMES THE TOOL TO USE INSTEAD. "not yours" alone leaves the caller to guess
		// whether the action is impossible or merely misplaced.
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodePermissionDenied,
			Message: fmt.Sprintf(
				"%s#%d was opened by %q, not by you (%q). Closing someone else's pull request needs "+
					"gh-pr-manage, which affects work they depend on and asks for confirmation.",
				repo, n, pr.Author, id.Login),
		}
	}

	closed, err := c.ClosePR(ctx, n)
	if err != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, err)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("closed your pull request #%d WITHOUT merging\n\n%s\n\n"+
			"The branch and its commits remain. This was your own proposal, so nothing anyone else "+
			"depended on changed.", closed.Number, closed.HTMLURL),
		Data: map[string]any{"pr": closed, "closed": "pull_request", "merged": false},
	}, nil
}

/**
 * prManage closes or merges a pull request opened by ANYONE.
 *
 * EVERY ACTION HERE AFFECTS SOMEONE ELSE, which is why the whole tool is `medium`: closing discards work
 * a person is relying on, and merging changes a branch others have built on. Both deserve a confirmation
 * before they run, and neither belongs beside the tools that act on your own work.
 *
 * THE TWO ACTIONS SHARE ONE TOOL because they share a blast radius and an audience. Splitting them would
 * produce two tools with the same effect and the same permission, differing only in a verb — which is a
 * distinction the model would have to learn without gaining anything.
 */
func (t *toolset) prManage(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	repo, err := required(args, "repo")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := t.requireRepo(ctx, PermMaintainer, repo); err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "pull_number")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"pull_number" is required`)
	}
	action := strings.TrimSpace(argStr(args, "action"))
	if action != "close" && action != "merge" {
		return extension.ToolResultData{}, errArg(`"action" must be "close" or "merge"`)
	}
	c, _, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}

	if action == "close" {
		closed, cerr := c.ClosePR(ctx, n)
		if cerr != nil {
			return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, cerr)
		}
		return extension.ToolResultData{
			Content: fmt.Sprintf("closed pull request #%d WITHOUT merging\n\n%s\n\n"+
				"This abandons someone's proposal; the branch and its commits still exist.",
				closed.Number, closed.HTMLURL),
			Data: map[string]any{"pr": closed, "closed": "pull_request", "merged": false},
		}, nil
	}

	merged, merr := c.MergePR(ctx, n, argStr(args, "merge_method"), "", argStr(args, "comment"))
	if merr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, merr)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("merged #%d\n%s", merged.Number, merged.HTMLURL),
		Data:    map[string]any{"pr": merged},
	}, nil
}

// Repository administration.

func (t *toolset) repoCreate(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	name, err := required(args, "name")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	org := argStr(args, "org")
	slug := name
	if org != "" {
		slug = org + "/" + name
	}
	if err := t.requireRepo(ctx, PermOrgAdmin, slug); err != nil {
		return extension.ToolResultData{}, err
	}
	c, err := t.accountClient(tenant)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	r, err := c.CreateRepo(ctx, ghclient.CreateRepoOptions{
		Org: org, Name: name,
		Description: argStr(args, "description"),
		Private:     argBool(args, "private"),
		AutoInit:    argBool(args, "auto_init"),
	})
	if err != nil {
		return extension.ToolResultData{}, errFailed("create %s: %v", slug, err)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("created %s (default branch: %s)", r.FullName, r.DefaultBranch),
		Data:    map[string]any{"repo": r},
	}, nil
}

func (t *toolset) repoRename(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	newName, err := required(args, "name")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	r, err := c.RenameRepo(ctx, newName)
	if err != nil {
		return extension.ToolResultData{}, errFailed("rename %s: %v", repo, err)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("renamed %s -> %s", repo, r.FullName),
		Data:    map[string]any{"repo": r},
	}, nil
}

func (t *toolset) repoDelete(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if err := t.requireRepo(ctx, PermMaintainer, repo); err != nil {
		return extension.ToolResultData{}, err
	}

	// Check the scope BEFORE calling: GitHub answers 403 for both "no permission" and "not
	// found" on DELETE, so a missing scope would read as "already gone".
	if _, sc, ierr := c.IdentityAndScopes(ctx); ierr == nil {
		if cerr := ghclient.CheckTokenCanDelete(sc); cerr != nil {
			return extension.ToolResultData{}, errArg(cerr.Error())
		}
	}

	// The caller must spell the repository out. Deleting is irreversible and the usual failure
	// is a wrong slug, not a wrong decision.
	if confirm := argStr(args, "confirm"); confirm != repo {
		return extension.ToolResultData{}, errArg(fmt.Sprintf(
			"refusing to delete %s: pass confirm=%q to confirm", repo, repo))
	}
	if err := c.DeleteRepo(ctx); err != nil {
		return extension.ToolResultData{}, errFailed("delete %s: %v", repo, err)
	}
	return extension.ToolResultData{Content: "deleted " + repo}, nil
}

// shortSHA abbreviates a commit sha for display. Tool output is read by a model and a human;
// both recognise 7 characters and neither benefits from 40.
func shortSHA(s string) string {
	if len(s) > 7 {
		return s[:7]
	}
	return s
}
