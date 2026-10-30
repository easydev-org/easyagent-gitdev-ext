package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	bus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/agent"
)

// runner calls tools and remembers what happened.
type runner struct {
	ctx     context.Context
	a       *agent.Agent
	extID   string
	tenant  string
	session string
	repo    string
	// ciRepo is where the CI tools are exercised. It defaults to repo, but a repository with no
	// workflow would leave three tools unverified — and 'skipped because there was nothing to look
	// at' is exactly the shape of coverage that quietly disappears.
	ciRepo  string
	mirror  string
	// forgeBase is the scheme+host of the mirror, so a remote can be built as an address.
	forgeBase string
	only       string
	verbose    bool
	skipGitHub bool

	// selfRunID is the CI run THIS SUITE is executing in, when there is one.
	//
	// THE SUITE MUST NOT TOUCH IT. The CI tools are exercised against a real repository, and that
	// repository is often the one running the suite — so a cancel or rerun aimed at 'the first active
	// run' aimed at ITSELF. It did: the job died with 'the operation was canceled' thirty seconds
	// into the GitHub half, which reads like a product failure and was the test shooting its own foot.
	selfRunID int64

	// artifactID is carried from the listing step to the download step: the second needs an id the
	// first discovered, and re-listing to find it again would be a second call for one fact.
	artifactID int64

	steps  []struct {
		name string
		fn   func(*state) error
	}
	passed []string
	failed []failure
	// skipped records tools that a deployment may legitimately not exercise — an empty artifact
	// list, a package registry with nothing published. Skipping is not failing, but it is reported:
	// a suite that silently covers less than it claims is worse than one that says so.
	skipped []string
}

type failure struct {
	step string
	err  string
}

// result is one tool reply, normalised.
type result struct {
	Content string
	Data    map[string]any
	Error   string
}

// call invokes one tool over the bus.
func (r *runner) call(tool string, args map[string]any) (result, error) {
	if args == nil {
		args = map[string]any{}
	}
	// EVERY CALL GETS ITS OWN DEADLINE. Sharing the suite's overall context means one tool that hangs
	// — a push to a host this container cannot reach, say — consumes the whole budget and the run is
	// cancelled with no result at all. A per-call ceiling turns that into one failed step with a
	// message, which is the difference between a diagnosable run and an empty one.
	ctx, cancel := context.WithTimeout(r.ctx, perCallTimeout)
	defer cancel()
	// THE GRANTS ARE THE POINT of testing through the bus: a tool whose permission the caller does
	// not hold is refused by the EXTENSION, not by the handler, so a grant that is wrong shows up
	// here and nowhere in a direct handler test.
	// THE GRANTS ARE PART OF WHAT IS UNDER TEST. A tool whose permission the caller does not hold is
	// refused by the EXTENSION, never reaching the handler — so this list is the one place the suite
	// can see a permission that is wrong, and a direct handler test cannot see it at all.
	// A WILDCARD ON THE OWNER, because the destructive group creates a repository and then renames and
	// deletes THAT one — a grant naming only the repository under test would refuse those, and the
	// refusal would look like a broken tool rather than an under-scoped test.
	grants := []*bus.Grant{
		{Key: "repo.maintainer", Params: bus.Struct(map[string]any{"repo": r.owner() + "/*"})},
		{Key: "github.repo", Params: bus.Struct(map[string]any{"repo": r.owner() + "/*"})},
		{Key: "org.admin", Params: bus.Struct(map[string]any{"org": r.owner()})},
	}
	tr, err := r.a.CallToolWithGrants(ctx, r.tenant, r.session, r.extID, tool,
		fmt.Sprintf("e2e-%s-%d", tool, time.Now().UnixNano()), args, grants)
	if err != nil {
		return result{}, fmt.Errorf("transport: %w", err)
	}
	out := result{Content: tr.Content}
	if tr.Data != nil {
		if m, ok := tr.Data.(map[string]any); ok {
			out.Data = m
		}
	}
	if tr.Error != nil {
		out.Error = tr.Error.Message
	}
	if r.verbose {
		j, _ := json.MarshalIndent(map[string]any{
			"tool": tool, "args": args, "error": out.Error,
			"content": truncate(out.Content, 300), "data": out.Data,
		}, "", "  ")
		fmt.Println(string(j))
	}
	return out, nil
}

// perCallTimeout bounds one tool call.
//
// TWO MINUTES: a git fetch of a large repository over a slow link is the slowest thing here, and
// anything past this is a tool talking to a host it cannot reach. Without a bound the suite's own
// deadline is the only limit, and it ends the RUN rather than the STEP.
const perCallTimeout = 2 * time.Minute

// must calls a tool and fails the step on a tool error.
func (r *runner) must(step, tool string, args map[string]any) (result, error) {
	res, err := r.call(tool, args)
	if err != nil {
		return res, err
	}
	if res.Error != "" {
		return res, fmt.Errorf("%s: %s", tool, res.Error)
	}
	return res, nil
}

func (r *runner) ok(step string) { r.passed = append(r.passed, step) }

func (r *runner) skip(step, why string) {
	r.skipped = append(r.skipped, step+" ("+why+")")
}

func (r *runner) fail(step string, err error) {
	r.failed = append(r.failed, failure{step: step, err: err.Error()})
	fmt.Printf("FAIL  %-40s %v\n", step, err)
}

// summary prints the tally and exits non-zero on any failure.
func (r *runner) summary() {
	fmt.Println()
	fmt.Printf("passed  %d\n", len(r.passed))
	fmt.Printf("skipped %d\n", len(r.skipped))
	for _, s := range r.skipped {
		fmt.Printf("        - %s\n", s)
	}
	fmt.Printf("failed  %d\n", len(r.failed))
	for _, f := range r.failed {
		fmt.Printf("        - %-38s %s\n", f.step, f.err)
	}
	if len(r.failed) > 0 {
		os.Exit(1)
	}
}

/**
 * cleanup removes what the run created, using the TOOLS, on the way out.
 *
 * IT RUNS IN A DEFER because a step that fails still leaves things behind and the next run inherits
 * them. Both failure modes that put twenty-eight stray repositories on the test organisation were
 * this: a delete that failed for want of a retry, and a run cancelled before the delete step was
 * reached at all.
 *
 * IT ONLY TOUCHES WHAT THIS RUN NAMED. Every id here was written by a step above; nothing is found by
 * pattern, because a cleanup that deletes by pattern has somebody else's repository as its worst case.
 *
 * ERRORS ARE PRINTED, NOT FATAL: the suite's verdict is already decided, and a failed tidy-up must not
 * turn a passing run red — but silence would hide the leak this exists to prevent.
 */
func (r *runner) cleanup(st *state) {
	if st.Tag != "" {
		if _, err := r.call("gh-tag-delete", map[string]any{
			"repo": r.repo, "tag": st.Tag, "confirm": true,
		}); err != nil {
			fmt.Printf("cleanup: delete tag %s: %v\n", st.Tag, err)
		}
	}
	// THE BRANCH THIS RUN PUSHED. stepBranchDelete normally removes it; this is the path taken when
	// that step never ran, and a branch left on the upstream is what makes a LATER run fail with
	// "cannot lock ref: reference already exists".
	if st.Branch != "" {
		if _, err := r.call("git-branch-delete", map[string]any{
			"remote":   r.mirrorRemote(),
			"branch":   st.Branch,
			"upstream": fmt.Sprintf("https://github.com/%s.git", r.repo),
			"confirm":  st.Branch,
		}); err != nil {
			fmt.Printf("cleanup: delete branch %s: %v\n", st.Branch, err)
		}
	}
	// THE THROWAWAY REPOSITORY, under whichever name it currently has: a rename may have happened and
	// the delete may not.
	name := st.ScratchRenamed
	if name == "" {
		name = st.Scratch
	}
	if name != "" {
		slug := r.owner() + "/" + name
		if _, err := r.call("gh-repo-delete", map[string]any{
			"repo": slug, "confirm": slug,
		}); err != nil {
			fmt.Printf("cleanup: delete repository %s: %v\n", slug, err)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// str reads a string field from a reply's data.
func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

// num reads a number that may have arrived as int, int64 or float64 — JSON over the bus does not
// promise which.
func num(m map[string]any, k string) int64 {
	switch v := m[k].(type) {
	case float64:
		return int64(v)
	case int64:
		return v
	case int:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	}
	return 0
}

// list reads an array of objects, whichever spelling its fields use.
func list(m map[string]any, k string) []map[string]any {
	raw, ok := m[k].([]any)
	if !ok {
		if already, ok := m[k].([]map[string]any); ok {
			return already
		}
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, x := range raw {
		if o, ok := x.(map[string]any); ok {
			out = append(out, o)
		}
	}
	return out
}

// sortedKeys is for a readable dump of a reply that has no schema.
func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// contains reports whether any string in the reply mentions `want` — used where the tool answers
// with prose rather than fields.
func contains(res result, want string) bool {
	if strings.Contains(res.Content, want) {
		return true
	}
	j, _ := json.Marshal(res.Data)
	return strings.Contains(string(j), want)
}
