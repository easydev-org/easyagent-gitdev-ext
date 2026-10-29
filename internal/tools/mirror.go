package tools

import (
	"context"
	"fmt"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forgeconfig"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

/**
 * The branch-and-commit tools.
 *
 * THERE IS NO WORKSPACE AND NO CLONE. Every operation is one forge API call: a branch is a ref, a
 * file is an object, and a change is a commit the forge builds. That is why these replace the old
 * staging area outright — the staging area existed to avoid a clone, and there is no clone left to
 * avoid.
 *
 * `git-file-patch` is the ONLY writer. Read and list are separate; there is deliberately no
 * write/delete/move trio, because one way to change a file means one way to get it wrong.
 */

/**
 * branchCreate makes a working branch from either a local branch or an upstream one.
 *
 * A NAME THAT ALREADY EXISTS IS AN ERROR BY DEFAULT. It used to be accepted silently, and that was
 * the worst of the options: a caller asking for a fresh branch would get the OLD one without being
 * told, patch it, push it, and update a pull request they believed was new. Saying no costs one
 * retry; saying nothing costs a wrong commit on somebody's review.
 *
 * `from` may be:
 *
 *   "main"                     a branch in this mirror
 *   "upstream:probe/loop"      a branch on the upstream, even one the mirror has never seen
 *
 * The upstream form needs `upstream` (the git URL) so the right token can be looked up. It exists
 * because a retry often needs to start from the branch that is already open rather than from main —
 * branching from main would silently discard that base.
 *
 * `on_exists` decides what a name collision means:
 *
 *   fail   (default) refuse, naming the commit that occupies the name
 *   reuse  accept it and continue on that branch
 *   reset  delete and recreate from `from` — discards whatever was there
 */
func (t *toolset) branchCreate(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	from := strings.TrimSpace(argStr(args, "from"))
	onExists := strings.ToLower(strings.TrimSpace(argStr(args, "on_exists")))
	if branch == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "branch is required",
		}
	}
	if from == "" {
		from = "main"
	}
	if onExists == "" {
		onExists = "fail"
	}
	if onExists != "fail" && onExists != "reuse" && onExists != "reset" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: `on_exists must be "fail", "reuse" or "reset"`,
		}
	}
	// ARGUMENT CHECKS BEFORE ANY NETWORK. Building the client already reaches the forge (it asks for
	// the API version), so a request that cannot work would spend a round trip to learn what the
	// arguments already said.
	upstreamBranch, wantsUpstream := strings.CutPrefix(from, "upstream:")
	upstreamURL := strings.TrimSpace(argStr(args, "upstream"))
	if wantsUpstream && upstreamURL == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: `from "upstream:<branch>" also needs upstream (the git URL, ` +
				`e.g. https://github.com/owner/repo.git)`,
		}
	}
	cl, cfg, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}

	exists, existingSHA, berr := cl.BranchExists(ctx, branch)
	if berr != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("check %q: %v", branch, berr)}
	}
	if exists {
		switch onExists {
		case "fail":
			return extension.ToolResultData{}, &extension.TypedError{
				Code: easybus.ToolResultErrorCodeInvalidArgument,
				Message: fmt.Sprintf(
					"branch %q already exists at %s. Pass on_exists=reuse to continue on it, "+
						"on_exists=reset to discard and recreate it, or choose another name",
					branch, short(existingSHA)),
			}
		case "reuse":
			return extension.ToolResultData{
				Content: fmt.Sprintf("%s already existed at %s (reusing)", branch, short(existingSHA)),
				Data: map[string]any{
					"branch": branch, "from": from, "existed": true, "commit": existingSHA,
					"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
				},
			}, nil
		case "reset":
			// Said plainly before it happens: this discards whatever the branch held.
			if derr := cl.DeleteBranch(ctx, branch); derr != nil {
				return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("delete %q: %v", branch, derr)}
			}
		}
	}

	var commit string
	var srcDesc string
	if wantsUpstream {
		upTok := upstreamToken(cfg, upstreamURL)
		sha, cerr := cl.CreateBranchFromUpstream(ctx, branch, upstreamURL, upstreamBranch, upTok)
		if cerr != nil {
			return extension.ToolResultData{}, &extension.TypedError{
				Code: easybus.ToolResultErrorCodeBusiness, Message: cerr.Error(),
			}
		}
		commit = sha
		srcDesc = "upstream " + upstreamBranch
	} else {
		if cerr := cl.CreateBranch(ctx, branch, from); cerr != nil && cerr != forge.ErrBranchExists {
			return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("create branch %q: %v", branch, cerr)}
		}
		_, sha, berr := cl.BranchExists(ctx, branch)
		if berr != nil {
			return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("read %q: %v", branch, berr)}
		}
		commit = sha
		srcDesc = from
	}

	return extension.ToolResultData{
		Content: fmt.Sprintf("%s created from %s at %s", branch, srcDesc, short(commit)),
		Data: map[string]any{
			"branch": branch, "from": from, "existed": exists, "commit": commit,
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}

// filePatch applies a patch and commits it as ONE commit.
//
// The commit message is required. A commit nobody can read in a log is a commit that has to be
// opened to be understood, and the caller always knows why it is changing the file.
func (t *toolset) filePatch(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	message := strings.TrimSpace(argStr(args, "message"))
	patch := argStr(args, "patch")
	if branch == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "branch is required",
		}
	}
	if message == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "message is required (it becomes the commit message)",
		}
	}
	if strings.TrimSpace(patch) == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "patch is required",
		}
	}
	cl, _, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}

	// Nothing is written until every hunk has been proven to apply: PlanPatch resolves the whole
	// change against the branch's current content, and only then does the commit happen. A rejected
	// patch therefore leaves the branch untouched rather than half-edited.
	plan, sha, aerr := cl.ApplyPatch(ctx, branch, message, patch)
	if aerr != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: aerr.Error(),
		}
	}

	var b strings.Builder
	for _, ch := range plan {
		verb := map[string]string{"add": "created", "update": "updated", "delete": "deleted"}[ch.Kind]
		fmt.Fprintf(&b, "%-8s %s\n", verb, ch.Path)
	}
	fmt.Fprintf(&b, "\n%d file(s) changed in one commit %s", len(plan), short(sha))

	rows := make([]map[string]any, 0, len(plan))
	for _, ch := range plan {
		rows = append(rows, map[string]any{"kind": ch.Kind, "path": ch.Path})
	}
	return extension.ToolResultData{
		Content: b.String(),
		Data: map[string]any{
			"commit": sha, "branch": branch, "changes": rows, "count": len(plan),
			"card": cardFor(cardTemplateCommit),
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}

// fileRead reads one file from one branch, with line numbers.
//
// Line numbers are in the TEXT because that is what a caller quotes back in a patch hunk header;
// the Data carries the raw content, because that is what a program wants.
func (t *toolset) fileRead(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	path := strings.TrimSpace(argStr(args, "path"))
	if path == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "path is required",
		}
	}
	cl, _, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	f, rerr := cl.ReadFile(ctx, path, branch)
	if rerr != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("%v", rerr)}
	}
	return extension.ToolResultData{
		Content: numberLines(f.Content),
		Data: map[string]any{
			"path": f.Path, "branch": branch, "content": f.Content, "size": f.Size,
			"card": cardFor(cardTemplateFile),
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, path), f.Path),
		},
	}, nil
}

// fileList lists one directory of one branch.
func (t *toolset) fileList(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	dir := strings.Trim(argStr(args, "dir"), "/")
	cl, _, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	entries, lerr := cl.ListDir(ctx, dir, branch)
	if lerr != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("%v", lerr)}
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name
		if e.Dir {
			name += "/"
		}
		fmt.Fprintf(&b, "%s\n", name)
	}
	if len(entries) == 0 {
		b.WriteString("(empty)")
	}
	rows := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, map[string]any{"path": e.Path, "name": e.Name, "dir": e.Dir, "size": e.Size})
	}
	return extension.ToolResultData{
		Content: strings.TrimRight(b.String(), "\n"),
		Data: map[string]any{
			"dir": dir, "branch": branch, "entries": rows, "count": len(rows),
			"card": cardFor(cardTemplateFiles),
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}

/**
 * syncUpstream fast-forwards a branch to its counterpart on the upstream.
 *
 * FOR PROTECTED BRANCHES. A branch that only moves by merges upstream can be followed safely; a
 * working branch cannot, because its commits exist nowhere else and following upstream would drop
 * them. This refuses rather than overwriting, and names both shas so the caller can look.
 */
func (t *toolset) syncUpstream(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	upstream := strings.TrimSpace(argStr(args, "upstream"))
	if branch == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument, Message: "branch is required",
		}
	}
	cl, cfg, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	if upstream == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: "upstream is required (the git URL to follow, e.g. https://github.com/owner/repo.git)",
		}
	}
	// The upstream's credential comes from the same `servers` table, keyed by host: a repository
	// mirrored from GitHub needs the GitHub entry's token, not the local forge's.
	upTok := upstreamToken(cfg, upstream)

	moved, local, up, serr := cl.SyncUpstream(ctx, branch, upstream, upTok)
	if serr != nil {
		return extension.ToolResultData{}, &extension.TypedError{Code: easybus.ToolResultErrorCodeBusiness, Message: fmt.Sprintf("%v", serr)}
	}
	msg := fmt.Sprintf("%s already at %s", branch, short(local))
	if moved {
		msg = fmt.Sprintf("%s fast-forwarded %s -> %s", branch, short(local), short(up))
	}
	return extension.ToolResultData{
		Content: msg,
		Data: map[string]any{
			"branch": branch, "moved": moved, "before": local, "after": up, "upstream": upstream,
			"card": cardFor(cardTemplateSync),
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}

/**
 * pushBranch sends one branch to the upstream, under a chosen name.
 *
 * The destination name defaults to the source's, which is what a mirror wants; a caller that wants
 * to keep a fixed name upstream (so re-pushing updates the same pull request) passes `to`.
 */
func (t *toolset) pushBranch(ctx context.Context, args map[string]any, session, tenant string) (extension.ToolResultData, error) {
	remote := argStr(args, "remote")
	branch := strings.TrimSpace(argStr(args, "branch"))
	upstream := strings.TrimSpace(argStr(args, "upstream"))
	to := strings.TrimSpace(argStr(args, "to"))
	if branch == "" || upstream == "" {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeInvalidArgument,
			Message: "branch and upstream are required",
		}
	}
	cl, cfg, err := t.remoteClient(session, tenant, remote)
	if err != nil {
		return extension.ToolResultData{}, err
	}
	// THE UPSTREAM'S CREDENTIAL IS NOT NECESSARILY IN `servers`: that table describes the MIRROR. A
	// deployment configures GitHub under `token`, so looking only in `servers` sent this push out
	// anonymous and GitHub refused it.
	upTok := upstreamToken(cfg, upstream)
	sha, perr := cl.PushBranch(ctx, branch, upstream, to, upTok)
	if perr != nil {
		return extension.ToolResultData{}, &extension.TypedError{
			Code: easybus.ToolResultErrorCodeBusiness, Message: perr.Error(),
		}
	}
	dest := to
	if dest == "" {
		dest = branch
	}
	return extension.ToolResultData{
		Content: fmt.Sprintf("pushed %s -> %s (%s)", branch, dest, short(sha)),
		Data: map[string]any{
			"branch": branch, "to": dest, "commit": sha, "upstream": upstream,
			"view": mirrorView(cl.Owner, cl.Repo, atBranch(branch, ""), cl.Owner+"/"+cl.Repo+" @ "+branch),
		},
	}, nil
}

// short is the first 12 characters of a sha, for a message that stays readable.
func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

// hostOf is the hostname of a git URL, for looking up the right server entry.
func hostOf(raw string) string { return forgeconfig.HostOf(raw) }

/**
 * upstreamToken picks the credential for a URL that is NOT the mirror.
 *
 * TWO SOURCES, IN ORDER, because a deployment configures them separately: `servers` describes the
 * MIRROR hosts this extension writes to, and `token` is the GitHub credential it reads and pushes
 * through. An upstream URL is almost never in `servers` — GitHub is not a mirror — so looking only
 * there found nothing and the push went out ANONYMOUS, which GitHub refuses with "no anonymous write
 * access". A host that IS listed still wins, so a deployment that lists its upstream keeps working.
 */
func upstreamToken(cfg forgeconfig.Config, upstream string) string {
	if srv, ok := cfg.ServerForHost(hostOf(upstream)); ok {
		return srv.Token
	}
	return cfg.UpstreamToken
}
