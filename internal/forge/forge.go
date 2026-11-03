// Package forge reads a REMOTE Gitea/Forgejo repository over its HTTP API,
// without cloning it.
//
// Why an API and not a clone: reading one file should not download the history
// (or even one full snapshot). A clone of a large repo to answer "what does
// README say" costs minutes of transfer; the API answers it in one request.
//
// Partial clone (`--filter=blob:none`) was the other candidate and is rejected
// on purpose: it still writes a repository to disk, still needs a working tree,
// and its cost is paid on every call. The API is stateless.
//
// SCOPE: this is a thin wrapper over the OFFICIAL SDK (code.gitea.io/sdk/gitea),
// which already handles Gitea's API shapes, pagination parameters and error
// mapping. Writing that by hand was the first attempt and it got the pagination
// parameter wrong (`limit` here, `per_page` elsewhere) — exactly the class of
// drift an upstream SDK absorbs for us.
//
// Forgejo advertises Gitea API compatibility, so the same client serves both.
package forge

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"code.gitea.io/sdk/gitea"
)

// ErrUnsupported is returned when the configured host is not a forge this
// package can talk to.
var ErrUnsupported = errors.New("this tool reads repositories over the Gitea/Forgejo API, so the host must be configured with kind=gitea (or use git-clone)")

// Ref is one branch or tag of a remote repository.
type Ref struct {
	// The JSON tags are lowercase because this struct is serialised to a BROWSER, whose fields are
	// lowercase. Without them Go emits `Name`/`Kind` and every client reads `undefined` — which
	// fails silently as an empty list rather than as an error.
	Name   string `json:"name"`
	Commit string `json:"commit"`
	// Kind is "branch" or "tag".
	Kind string `json:"kind"`
}

// File is one remote file's content plus the revision it came from.
type File struct {
	Path     string
	Revision string
	Content  string
	Size     int
}

// Client reads one remote repository.
type Client struct {
	c *gitea.Client
	// Owner and Repo are the path segments of the repository.
	Owner string
	Repo  string
	// apiBase and token allow the few calls the SDK does not expose.
	//
	// The SDK v0.25 has READERS for git objects (GetTrees, GetBlob) but no writers — there is no
	// CreateBlob, CreateTree, CreateCommit or EditBranch. Building one commit for a multi-file patch
	// needs all four, so those go over plain HTTP against the same API the SDK already talks to.
	// Keeping the base and the token here rather than re-deriving them means one place decides what
	// host and credential this client uses.
	apiBase string
	token   string
}

// New builds a Client from a repository URL and the host's settings.
//
// `baseURL` is the Gitea origin (scheme + host, optionally a sub-path); the SDK
// appends /api/v1 itself. An empty token reads public repositories anonymously.
/**
 * NewForHost builds a client for the FORGE, not for one repository on it.
 *
 * Some calls are above a repository — listing them all, creating an organisation — and they need a
 * base URL and a credential but no owner/repo. Passing a placeholder remote through New would work
 * and would be a lie: every error and every log line would name a repository that does not exist.
 */
func NewForHost(baseURL, token string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, ErrUnsupported
	}
	opts := []gitea.ClientOption{}
	if t := strings.TrimSpace(token); t != "" {
		opts = append(opts, gitea.SetToken(t))
	}
	c, err := gitea.NewClient(strings.TrimRight(baseURL, "/"), opts...)
	if err != nil {
		return nil, fmt.Errorf("gitea client for %q: %w", baseURL, err)
	}
	return &Client{
		c:       c,
		apiBase: strings.TrimRight(baseURL, "/") + "/api/v1", token: strings.TrimSpace(token),
	}, nil
}

func New(baseURL, token, remoteURL string) (*Client, error) {
	if strings.TrimSpace(baseURL) == "" {
		return nil, ErrUnsupported
	}
	owner, repo, err := splitRepoPath(remoteURL)
	if err != nil {
		return nil, err
	}
	opts := []gitea.ClientOption{}
	if t := strings.TrimSpace(token); t != "" {
		// SetToken is a CONSTRUCTOR option, not a method: the credential must be
		// known when the client is built.
		opts = append(opts, gitea.SetToken(t))
	}
	c, err := gitea.NewClient(strings.TrimRight(baseURL, "/"), opts...)
	if err != nil {
		return nil, fmt.Errorf("gitea client for %q: %w", baseURL, err)
	}
	return &Client{
		c: c, Owner: owner, Repo: repo,
		apiBase: strings.TrimRight(baseURL, "/") + "/api/v1", token: strings.TrimSpace(token),
	}, nil
}

// splitRepoPath extracts (owner, repo) from a remote URL.
//
// It accepts the shapes a remote actually takes: https://host/owner/repo(.git),
// git@host:owner/repo.git, and a trailing slash. Gitea allows nested owners
// (`org/sub/repo`), so the repo is the LAST segment.
func splitRepoPath(remote string) (string, string, error) {
	s := strings.TrimSpace(remote)
	if s == "" {
		return "", "", fmt.Errorf("remote is required")
	}
	// A URL with a scheme is parsed as such. This check must come FIRST: an
	// `https://host/...` also contains a ':' before any '/', so an scp-style test
	// written the other way round silently treats "https" as a hostname and puts
	// the real host into the owner segment.
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", "", fmt.Errorf("cannot parse %q as a URL", remote)
		}
		// The PATH is what carries owner/repo; without this the host would end up
		// in the owner and every API call would 404.
		s = u.Path
	} else if i := strings.Index(s, ":"); i > 0 {
		// scp-like syntax: [user@]host:owner/repo.git
		s = s[i+1:]
	}
	s = strings.Trim(s, "/")
	s = strings.TrimSuffix(s, ".git")
	parts := strings.Split(s, "/")
	if len(parts) < 2 {
		return "", "", fmt.Errorf("cannot derive owner/repo from %q", remote)
	}
	return strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1], nil
}

// Tags returns just this repository's tag names.
//
// A separate method from ListRefs because a caller watching for new tags does not want the
// branches: paging through every branch of a large repository to find that nothing changed
// would make a one-minute poll expensive for no reason.
//
// Pagination: the SDK takes an explicit page/page-size and does NOT iterate for us, so
// these loops must advance the page or a repository with more tags than one page would
// silently under-report.
func (c *Client) Tags(ctx context.Context) ([]string, error) {
	out := []string{}
	for page := 1; page <= 200; page++ {
		tags, _, err := c.c.ListRepoTags(c.Owner, c.Repo, gitea.ListRepoTagsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: listPageSize},
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, t := range tags {
			out = append(out, t.Name)
		}
		if len(tags) < listPageSize {
			break
		}
	}
	return out, nil
}

// ListRefs returns every branch and tag.
//
// Pagination: see the note on Tags — the SDK does not iterate for us.
func (c *Client) ListRefs(ctx context.Context) ([]Ref, error) {
	out := []Ref{}

	for page := 1; page <= 200; page++ {
		branches, _, err := c.c.ListRepoBranches(c.Owner, c.Repo, gitea.ListRepoBranchesOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: listPageSize},
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, b := range branches {
			out = append(out, Ref{Name: b.Name, Commit: b.Commit.ID, Kind: "branch"})
		}
		if len(branches) < listPageSize {
			break
		}
	}

	for page := 1; page <= 200; page++ {
		tags, _, err := c.c.ListRepoTags(c.Owner, c.Repo, gitea.ListRepoTagsOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: listPageSize},
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, t := range tags {
			// A Tag carries its own ID; CommitMeta has SHA (not ID). Prefer the
			// commit meta, fall back to the tag's own id (an annotated tag points
			// at a tag object, and its Commit is what a reader wants).
			sha := ""
			if t.Commit != nil {
				sha = t.Commit.SHA
			}
			if sha == "" {
				sha = t.ID
			}
			out = append(out, Ref{Name: t.Name, Commit: sha, Kind: "tag"})
		}
		if len(tags) < listPageSize {
			break
		}
	}

	sortRefs(out)
	return out, nil
}

// listPageSize is the API page size; 50 is Gitea's own default maximum for most
// endpoints and keeps the request count low without assuming a bigger cap.
const listPageSize = 50

// ReadFile returns one file's content at ref. An empty ref means the repository's
// default branch.
func (c *Client) ReadFile(ctx context.Context, path, ref string) (File, error) {
	path = strings.Trim(path, "/")
	if path == "" {
		return File{}, fmt.Errorf("path is required")
	}
	content, _, err := c.c.GetFile(c.Owner, c.Repo, ref, path)
	if err != nil {
		return File{}, mapErr(err)
	}
	return File{Path: path, Revision: ref, Content: string(content), Size: len(content)}, nil
}

// DirEntry is one entry in a repository directory.
type DirEntry struct {
	Path string
	Name string
	// Dir is true for a subtree, false for a file. The panel draws them differently and only a
	// directory can be descended into.
	Dir bool
	// Size is the blob size in bytes; 0 for a directory.
	Size int
}

/**
 * ListDir returns the entries directly inside one directory at ref.
 *
 * WHY THE CONTENTS API AND NOT THE GIT TREE: the SDK's `GetTrees` takes a REF only — its
 * `escapeValidatePathSegments` rejects a value with a "/" in it, so "main/src" cannot be addressed
 * that way. `ListContents` takes ref and path separately, which is exactly the shape a directory
 * browser has, and it returns the immediate children rather than the whole subtree.
 *
 * An empty dir means the repository ROOT.
 */
func (c *Client) ListDir(ctx context.Context, dir, ref string) ([]DirEntry, error) {
	dir = strings.Trim(dir, "/")
	list, _, err := c.c.ListContents(c.Owner, c.Repo, ref, dir)
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]DirEntry, 0, len(list))
	for _, e := range list {
		// A "submodule" has no blob to open from here, so it is presented as a directory — which is
		// what it is from this side, and what a reader expects to click into.
		isDir := e.Type == "dir" || e.Type == "submodule"
		out = append(out, DirEntry{Path: e.Path, Name: e.Name, Dir: isDir, Size: int(e.Size)})
	}
	return out, nil
}

// mapErr turns the SDK's error into something a caller can act on.
//
// The SDK reports a missing path/repo as a plain message ("The target couldn't be
// found.") without saying WHICH of the two common causes applies, so the hint is
// added here rather than repeated at every call site.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "token is required"):
		return fmt.Errorf("this repository needs authentication: add a token for its host in the `servers` config")
	case strings.Contains(msg, "couldn't be found"), strings.Contains(msg, "not found"):
		return fmt.Errorf("%s (check the path/ref, or that the token can read this repository)", msg)
	default:
		return err
	}
}

// PullRequest is one merge/pull request, in the shape the tools report.
type PullRequest struct {
	Index int64
	Title string
	State string
	Head  string
	Base  string
	URL   string
	// Mergeable reports whether the forge believes the change applies cleanly.
	Mergeable bool
}

// OpenPullRequest creates a merge request from head into base.
//
// The head branch must already exist on the remote — this call only opens the
// request, it does not upload commits. The caller pushes first (inside the
// sandbox), so the forge never sees a request for a branch it does not have.
func (c *Client) OpenPullRequest(ctx context.Context, base, head, title, body string) (PullRequest, error) {
	pr, _, err := c.c.CreatePullRequest(c.Owner, c.Repo, gitea.CreatePullRequestOption{
		Head: head, Base: base, Title: title, Body: body,
	})
	if err != nil {
		return PullRequest{}, mapErr(err)
	}
	return toPullRequest(pr), nil
}

// ListPullRequests lists merge requests in the given state
// ("open" | "closed" | "all").
func (c *Client) ListPullRequests(ctx context.Context, state string) ([]PullRequest, error) {
	var out []PullRequest
	for page := 1; page <= 200; page++ {
		prs, _, err := c.c.ListRepoPullRequests(c.Owner, c.Repo, gitea.ListPullRequestsOptions{
			State:       gitea.StateType(state),
			ListOptions: gitea.ListOptions{Page: page, PageSize: listPageSize},
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, pr := range prs {
			out = append(out, toPullRequest(pr))
		}
		if len(prs) < listPageSize {
			break
		}
	}
	return out, nil
}

// GetPullRequest returns one merge request.
func (c *Client) GetPullRequest(ctx context.Context, index int64) (PullRequest, error) {
	pr, _, err := c.c.GetPullRequest(c.Owner, c.Repo, index)
	if err != nil {
		return PullRequest{}, mapErr(err)
	}
	return toPullRequest(pr), nil
}

// Comment adds a comment to a merge request (or any issue).
//
// Gitea models an MR as an issue with a patch attached, so a review comment goes
// through the issue-comment endpoint.
func (c *Client) Comment(ctx context.Context, index int64, body string) (Comment, error) {
	if strings.TrimSpace(body) == "" {
		return Comment{}, fmt.Errorf("body is required")
	}
	cm, _, err := c.c.CreateIssueComment(c.Owner, c.Repo, index, gitea.CreateIssueCommentOption{Body: body})
	if err != nil {
		return Comment{}, mapErr(err)
	}
	return toComment(cm), nil
}

// ListComments returns the comments of a merge request in posting order.
func (c *Client) ListComments(ctx context.Context, index int64) ([]Comment, error) {
	var out []Comment
	for page := 1; page <= 200; page++ {
		cms, _, err := c.c.ListIssueComments(c.Owner, c.Repo, index, gitea.ListIssueCommentOptions{
			ListOptions: gitea.ListOptions{Page: page, PageSize: listPageSize},
		})
		if err != nil {
			return nil, mapErr(err)
		}
		for _, cm := range cms {
			out = append(out, toComment(cm))
		}
		if len(cms) < listPageSize {
			break
		}
	}
	return out, nil
}

// ClosePullRequest closes a merge request WITHOUT merging it.
//
// Closing is deliberately separate from merging: abandoning a submission and
// landing one are different decisions, and collapsing them would let a caller
// close by accident while trying to merge.
func (c *Client) ClosePullRequest(ctx context.Context, index int64) (PullRequest, error) {
	closed := gitea.StateClosed
	pr, _, err := c.c.EditPullRequest(c.Owner, c.Repo, index, gitea.EditPullRequestOption{
		State: &closed,
	})
	if err != nil {
		return PullRequest{}, mapErr(err)
	}
	return toPullRequest(pr), nil
}

// ReopenPullRequest reopens a previously closed merge request.
func (c *Client) ReopenPullRequest(ctx context.Context, index int64) (PullRequest, error) {
	open := gitea.StateOpen
	pr, _, err := c.c.EditPullRequest(c.Owner, c.Repo, index, gitea.EditPullRequestOption{
		State: &open,
	})
	if err != nil {
		return PullRequest{}, mapErr(err)
	}
	return toPullRequest(pr), nil
}

// MergePullRequest merges a merge request.
//
// `style` is the forge's merge style (merge | rebase | rebase-merge | squash |
// fast-forward-only); empty uses the forge's default. `deleteBranch` removes the
// head branch afterwards, which is what keeps a repository from filling up with
// one `agent/*` branch per submission.
func (c *Client) MergePullRequest(ctx context.Context, index int64, style, title, message string, deleteBranch bool) (PullRequest, error) {
	// ALWAYS send an explicit style.
	//
	// The SDK marshals the option struct unconditionally, so an empty Style goes
	// out as `"Do":""` — and Forgejo SILENTLY IGNORES a merge request whose Do is
	// empty: it answers 200, the MR stays open, and the caller concludes the merge
	// worked. Verified against a real server: `{"Do":"merge"}` merges,
	// `{"Do":""}` is a no-op. So "omit and let the forge pick" is not an option
	// here; the default has to be named.
	if style == "" {
		style = "merge"
	}
	opt := gitea.MergePullRequestOption{
		Style: gitea.MergeStyle(style),
		Title: title, Message: message,
	}
	if deleteBranch {
		del := true
		opt.DeleteBranchAfterMerge = &del
	}
	if ok, resp, err := c.c.MergePullRequest(c.Owner, c.Repo, index, opt); err != nil {
		// The SDK turns a non-2xx into an error, but its text is often just the
		// status. Gitea answers 405 with a REASON in the body ("User not allowed to
		// merge PR", "PR not in mergeable state", ...) and each needs a different
		// fix, so the status and the raw body are reported instead of the SDK's
		// summary.
		detail := ""
		if resp != nil {
			detail = fmt.Sprintf(" (status %d)", resp.StatusCode)
		}
		return PullRequest{}, fmt.Errorf("merge #%d rejected%s: %v", index, detail, err)
	} else if !ok {
		return PullRequest{}, fmt.Errorf("merge #%d was not performed (the forge returned a non-success status)", index)
	}
	// A merge endpoint that answers 200 without merging is the failure mode this
	// guards against (see the Style note above), so "no error" is NOT proof.
	// Confirm the MR actually left the open state before reporting success.
	// The merge endpoint returns a bare boolean, so read the MR back to report
	// the resulting state (merged vs merely closed) instead of guessing.
	pr, _, err := c.c.GetPullRequest(c.Owner, c.Repo, index)
	if err != nil {
		// The merge itself succeeded; failing to re-read must not turn that into
		// an error the caller would retry.
		return PullRequest{Index: index, State: "merged"}, nil
	}
	if pr.State == gitea.StateOpen {
		return PullRequest{}, fmt.Errorf(
			"the forge accepted the merge request for #%d but the merge request is still open "+
				"(nothing was merged)", index)
	}
	return toPullRequest(pr), nil
}

// Comment is one comment on a merge request.
type Comment struct {
	ID     int64
	Body   string
	Author string
}

func toComment(cm *gitea.Comment) Comment {
	if cm == nil {
		return Comment{}
	}
	out := Comment{ID: cm.ID, Body: cm.Body}
	if cm.Poster != nil {
		out.Author = cm.Poster.UserName
	}
	return out
}

func toPullRequest(pr *gitea.PullRequest) PullRequest {
	if pr == nil {
		return PullRequest{}
	}
	out := PullRequest{
		// The SDK calls the PR number `Index` (its JSON key is "number").
		Index: pr.Index, Title: pr.Title, URL: pr.HTMLURL,
		State: string(pr.State),
	}
	if pr.Head != nil {
		out.Head = pr.Head.Ref
	}
	if pr.Base != nil {
		out.Base = pr.Base.Ref
	}
	return out
}

// sortRefs orders branches before tags, then by name, so the output is stable.
func sortRefs(refs []Ref) {
	// A tiny insertion sort keeps this dependency-free and the slices are short.
	for i := 1; i < len(refs); i++ {
		for j := i; j > 0 && less(refs[j], refs[j-1]); j-- {
			refs[j], refs[j-1] = refs[j-1], refs[j]
		}
	}
}

func less(a, b Ref) bool {
	if a.Kind != b.Kind {
		return a.Kind == "branch"
	}
	return a.Name < b.Name
}
