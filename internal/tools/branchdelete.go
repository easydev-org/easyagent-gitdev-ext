package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

/**
 * branchDelete removes a branch from the UPSTREAM.
 *
 * WHY IT EXISTS NOW: the suite that exercises every tool leaves a branch on the upstream each run, and
 * there was no way to remove one — so the branches accumulated until a run collided with a stale name
 * and failed for a reason that had nothing to do with the code. The tidy-up was going to be a script
 * outside the tool, which would have meant the capability existed and was deliberately not offered:
 * exactly the sort of gap that makes a caller reach for `git push --delete` by hand.
 *
 * IT DELETES ON THE UPSTREAM, NOT ON THE MIRROR. The mirror's own branches are its working state and
 * `git-branch-create` manages them; what needs removing after a change is the branch that was pushed
 * for review.
 *
 * THE `confirm` ARGUMENT IS THE BRANCH'S OWN NAME, like `gh-repo-delete`. Deletion is not reversible
 * from here, and a caller who cannot name what they are deleting has not decided to.
 */
func (t *toolset) branchDelete(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("deleting the branch")
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	upstream := strings.TrimSpace(argStr(args, "upstream"))
	if branch == "" || upstream == "" {
		return extension.ToolResultData{}, errArg(`"branch" and "upstream" are required: the branch to ` +
			`delete and the repository it was pushed to`)
	}
	if got := strings.TrimSpace(argStr(args, "confirm")); got != branch {
		return extension.ToolResultData{}, errArg(fmt.Sprintf(
			"refusing to delete %q: pass confirm=%q to confirm", branch, branch))
	}

	// The mirror client identifies WHICH forge configuration applies, even though the deletion happens
	// on the upstream: the credentials for both come from the same `servers` table, and the upstream's
	// is chosen by host.
	cl, cfg, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	upTok := upstreamToken(cfg, upstream)
	if derr := forge.DeleteUpstreamBranch(ctx, upstream, branch, upTok); derr != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeBusiness, Message: derr.Error(),
		}
	}

	return extension.ToolResultData{
		Content: fmt.Sprintf("deleted %s from %s", branch, upstream),
		Data: map[string]any{
			"branch": branch, "upstream": upstream,
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}
