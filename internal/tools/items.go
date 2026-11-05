package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/ghclient"
)

// ONE numbering space, so ONE set of tools.
//
// GitHub models a pull request as a kind of issue: `GET /repos/{o}/{r}/issues/{n}` returns a pull
// request when n is one, and a repository's issues and pull requests share a single counter
// (verified: #1..#5 pull requests, #6 issue, #7 issue, #8 pull request — one continuous series).
// There is therefore no such thing as "the issue numbered 8" and "the pull request numbered 8";
// there is one object with one number.
//
// That makes `gh-issue-show 8` unambiguous, and it means a caller does not have to know in advance
// which kind of object it is holding. These tools report the kind, and add the pull-request-only
// facts (head/base, mergeability) when there are any.
//
// The pull-request-only ACTIONS keep their own names — gh-pr-create, gh-pr-close, gh-pr-manage —
// because they do something an issue cannot: creating a pull request starts from a workspace rather
// than from a title, and closing one is a different act from closing an issue (see issueClose).

// issueRef resolves an issue or pull request by number and says which it is.
func (t *toolset) issueRef(ctx context.Context, c *ghclient.Client, number int) (ghclient.Issue, bool, error) {
	is, err := c.Issue(ctx, number)
	if err != nil {
		return ghclient.Issue{}, false, err
	}
	if !is.IsPullRequest {
		return is, false, nil
	}
	return is, true, nil
}

// issueShow shows an issue OR a pull request, saying which.
func (t *toolset) issueShow(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("reading the issue")
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "issue_id")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"issue_id" is required`)
	}
	is, isPR, ierr := t.issueRef(ctx, c, n)
	if ierr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, ierr)
	}

	var b strings.Builder
	kind := "issue"
	if isPR {
		kind = "pull request"
	}
	fmt.Fprintf(&b, "#%d %s  [%s]  (%s)\n", is.Number, is.Title, is.State, kind)
	fmt.Fprintf(&b, "by %s  %s\n", is.Author, is.CreatedAt)
	if len(is.Labels) > 0 {
		fmt.Fprintf(&b, "labels: %s\n", strings.Join(is.Labels, ", "))
	}
	fmt.Fprintf(&b, "%s\n", is.HTMLURL)

	// Pull-request-only facts, so a caller does not have to make a second call to learn whether
	// this can be merged.
	if isPR {
		if pr, perr := c.PR(ctx, n); perr == nil {
			fmt.Fprintf(&b, "\nbranch: %s -> %s\n", pr.Head, pr.Base)
			b.WriteString(renderMergeable(pr.Mergeable))
			b.WriteString("\nuse gh-pr-files / gh-pr-diff / gh-pr-checks for the change itself")
		}
	}

	if strings.TrimSpace(is.Body) != "" {
		fmt.Fprintf(&b, "\n---\n%s\n", strings.TrimSpace(is.Body))
	}
	Progress(ctx).Phase("reading its comments")
	comments, _ := c.IssueComments(ctx, n)
	if len(comments) > 0 {
		fmt.Fprintf(&b, "\n--- %d comment(s); see gh-issue-comments for the full thread ---\n", len(comments))
	}
	return extension.ToolResultData{
		Content: b.String(),
		Data: map[string]any{
			"issue": is, "is_pull_request": isPR, "comments": len(comments),
			// A pull request IS an issue here, so the card route carries both facts and lets the UI
			// choose which shape to draw.
			"card": cardFor(cardTemplateItem),
			// Only a pull request has a page. An issue would need one of its own, and offering a
			// route that does not exist is worse than offering no link.
			"view": issueView(repo, n, isPR, fmt.Sprintf("#%d %s", is.Number, is.Title)),
		},
	}, nil
}

// issueList lists issues and/or pull requests.
func (t *toolset) issueList(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("listing issues and pull requests")
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	// kind filters after the fetch, because GitHub's issues endpoint cannot filter by kind.
	// Unknown values are rejected rather than ignored: silently returning everything would look
	// like the filter worked.
	kind := argStr(args, "kind")
	switch kind {
	case "", "all", "issue", "pr":
	default:
		return extension.ToolResultData{}, errArg(`"kind" must be one of: all, issue, pr`)
	}
	items, lerr := c.ListIssuesBoth(ctx, ghclient.ListIssuesOptions{
		State: argStr(args, "state"), Label: argStr(args, "label"),
		Limit: argInt(args, "limit"),
	})
	if lerr != nil {
		return extension.ToolResultData{}, errFailed("%s: %v", repo, lerr)
	}

	var kept []ghclient.Issue
	for _, it := range items {
		switch kind {
		case "issue":
			if it.IsPullRequest {
				continue
			}
		case "pr":
			if !it.IsPullRequest {
				continue
			}
		}
		kept = append(kept, it)
	}
	if len(kept) == 0 {
		return extension.ToolResultData{
			Content: "(nothing matches)",
			Data:    map[string]any{"items": kept, "count": 0},
		}, nil
	}
	var b strings.Builder
	for _, i := range kept {
		mark := " "
		if i.State == "closed" {
			mark = "x"
		}
		label := "issue"
		if i.IsPullRequest {
			label = "PR   "
		}
		fmt.Fprintf(&b, "%s #%-4d %s %-52s %s", mark, i.Number, label, truncate(i.Title, 52), i.Author)
		if i.Comments > 0 {
			fmt.Fprintf(&b, "  (%d)", i.Comments)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "\n%d item(s)", len(kept))
	return extension.ToolResultData{
		Content: b.String(),
		Data:    map[string]any{"items": kept, "count": len(kept)},
	}, nil
}

// issueComment comments on an issue OR a pull request.
//
// With path and line it is a review comment on that line of the diff — which only a pull request
// has. On an issue that combination is REFUSED rather than quietly downgraded to a conversation
// comment: the caller asked for something specific and got something else.
func (t *toolset) issueComment(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("posting the comment")
	repo, err := required(args, "repo")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "issue_id")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"issue_id" is required`)
	}
	body, err := required(args, "body")
	if err != nil {
		return extension.ToolResultData{}, err
	}
	c, _, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}

	path := argStr(args, "path")
	line := argInt(args, "line")
	if path != "" || line > 0 {
		_, isPR, ierr := t.issueRef(ctx, c, n)
		if ierr != nil {
			return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, ierr)
		}
		if !isPR {
			return extension.ToolResultData{}, errArg(fmt.Sprintf(
				"#%d is an issue, not a pull request — it has no code lines to comment on. "+
					"Drop `path` and `line` to comment on the issue itself", n))
		}
	}

	cm, cerr := c.CreatePRComment(ctx, ghclient.CreatePRCommentOptions{
		Number: n, Body: body, Path: path, Line: line, Side: argStr(args, "side"),
	})
	if cerr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, cerr)
	}
	where := "the conversation"
	if cm.Path != "" {
		where = fmt.Sprintf("%s line %d", cm.Path, cm.Line)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("commented on #%d at %s\n%s", n, where, cm.URL),
		Data:    map[string]any{"issue": n, "comment": cm.ID, "url": cm.URL, "path": cm.Path, "line": cm.Line},
	}, nil
}

// issueComments shows everything said on an issue or pull request.
func (t *toolset) issueComments(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("reading the conversation")
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "issue_id")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"issue_id" is required`)
	}
	convo, cerr := c.IssueComments(ctx, n)
	if cerr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, cerr)
	}
	// Line comments exist only on pull requests; on an issue this simply returns nothing.
	Progress(ctx).Phase("reading line comments")
	inline, _ := c.PRComments(ctx, n)

	if len(convo) == 0 && len(inline) == 0 {
		return extension.ToolResultData{
			Content: fmt.Sprintf("no comments on #%d", n),
			Data:    map[string]any{"conversation": convo, "inline": inline, "count": 0},
		}, nil
	}
	var b strings.Builder
	if len(convo) > 0 {
		fmt.Fprintf(&b, "=== conversation (%d) ===\n", len(convo))
		for _, cm := range convo {
			fmt.Fprintf(&b, "\n[%s] %s\n%s\n", cm.Author, cm.When, strings.TrimSpace(cm.Body))
		}
	}
	if len(inline) > 0 {
		fmt.Fprintf(&b, "\n=== line comments (%d) ===\n", len(inline))
		for _, cm := range inline {
			loc := cm.Path
			if cm.Line > 0 {
				loc = fmt.Sprintf("%s:%d", cm.Path, cm.Line)
			}
			fmt.Fprintf(&b, "\n[%s] %s  @ %s\n%s\n", cm.Author, cm.When, loc, strings.TrimSpace(cm.Body))
		}
	}
	return extension.ToolResultData{
		Content: b.String(),
		Data: map[string]any{
			"conversation": convo, "inline": inline,
			"count": len(convo) + len(inline),
		},
	}, nil
}

/**
 * issueClose closes an ISSUE. It refuses a pull request.
 *
 * THE TWO USED TO SHARE THIS ENDPOINT, and the tool reported which one it had closed. That was convenient
 * and wrong: a pull request is a proposal, and abandoning one is a different act with a different
 * audience and a different effect on other people. Routing it through an "issue" tool meant the caller
 * never declared which of the two it believed it was doing, and the effect could not be declared honestly
 * either — an issue close is `low`, a pull-request close by someone else is `medium`.
 *
 * The refusal names where to go instead, because "that is a pull request" alone leaves the caller to
 * guess whether the tool is wrong or the request is.
 */
func (t *toolset) issueClose(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	Progress(ctx).Phase("closing")
	c, repo, err := t.clientFor(tenant, args)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	n := argInt(args, "issue_id")
	if n <= 0 {
		return extension.ToolResultData{}, errArg(`"issue_id" is required`)
	}
	_, isPR, ierr := t.issueRef(ctx, c, n)
	if ierr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, ierr)
	}
	if isPR {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: fmt.Sprintf(
				"%s#%d is a PULL REQUEST, not an issue. Use gh-pr-close for your own, or gh-pr-manage "+
					"for anyone's — they differ in what they affect and in what they ask of you.",
				repo, n),
		}
	}
	if cerr := c.CloseIssue(ctx, n); cerr != nil {
		return extension.ToolResultData{}, errFailed("%s#%d: %v", repo, n, cerr)
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("closed issue #%d", n),
		Data:    map[string]any{"issue": n, "closed": "issue"},
	}, nil
}

// renderMergeable turns the tri-state into three distinguishable sentences.
//
// Mergeable is a *bool because GitHub computes it asynchronously: nil means "not worked out yet",
// which is a different instruction to the caller than false. Printing the pointer with %v emitted
// an address ("0x3bbecddc62d0"), which reads as a value and is not one.
func renderMergeable(v *bool) string {
	switch {
	case v == nil:
		return "mergeable: unknown (GitHub is still computing it; ask again shortly)\n"
	case *v:
		return "mergeable: yes\n"
	default:
		return "mergeable: NO — conflicts with the base branch, or a required check failed\n"
	}
}
