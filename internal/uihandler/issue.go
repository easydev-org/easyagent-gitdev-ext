package uihandler

import (
	"context"
)

/**
 * IssueSource reads an ISSUE from GitHub for the UI's issue pages.
 *
 * WHY AN ISSUE NEEDS A PAGE AT ALL: a pull request already has one, and an issue is the same kind of
 * thing — a number, a title, a body, a conversation — that a reader wants to read in full rather
 * than skim in a card. Without this, `gh-issue-show` produced a card and no way to open it, so a
 * result that pointed at a pull request offered a button and one that pointed at an issue did not,
 * for no reason the reader could see.
 *
 * Declared as an interface for the same reason PullSource is: this package must not depend on the
 * GitHub client, and a deployment without a token simply leaves it nil.
 */
type IssueSource interface {
	// Issue returns the issue's own facts.
	Issue(ctx context.Context, repo string, number int) (IssueView, error)
	// Comments returns its conversation, oldest first.
	Comments(ctx context.Context, repo string, number int) ([]CommentView, error)
	// List returns issues and/or pull requests.
	//
	// ONE METHOD FOR BOTH because GitHub numbers them in one sequence and the two lists are the same
	// query with a different filter. Two methods would be two places to keep the paging and the
	// filtering right.
	List(ctx context.Context, repo string, kind, state string, limit int) ([]IssueView, error)
}

// IssueView is the issue as the page shows it.
type IssueView struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"`
	Body   string `json:"body"`
	Author string `json:"author"`
	URL    string `json:"url"`
	// Labels are the issue's own labels, in the order GitHub returns them.
	Labels []string `json:"labels"`
	// IsPullRequest is reported rather than hidden: GitHub numbers issues and pull requests in ONE
	// sequence, so a reader who opens "/issue/3" on a pull request should be told what they are
	// looking at rather than shown a page that quietly omits the code.
	IsPullRequest bool `json:"isPullRequest"`
	// Comments is the count, so a list can show it without fetching each conversation.
	Comments int `json:"comments"`
}

// CommentView is one message in an issue's conversation.
type CommentView struct {
	ID     int64  `json:"id"`
	Author string `json:"author"`
	Body   string `json:"body"`
	When   string `json:"when"`
}
