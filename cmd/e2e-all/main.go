// Call EVERY tool this extension registers, against a running instance, over NATS.
//
// WHY THIS EXISTS, AND WHY IT IS NOT A GO TEST: the tests in internal/tools call Registry directly.
// That proves a handler works and says nothing about whether the extension would ever be ASKED —
// registration, permissions, deserialisation of arguments, and the reply's shape all happen outside
// the handler. This drives the deployed thing the way the product does: a request on the bus, a
// result on the bus.
//
// IT RUNS IN DEPENDENCY ORDER, because these tools form a workflow: a branch must exist before a
// patch can land on it, a patch before a push, a push before a pull request. Each step therefore
// reuses what the previous one produced, which is what makes a failure point at the step that broke
// rather than at "the suite".
//
// EVERY TOOL IS ASSERTED, not merely called: a tool that returns 200 and writes nothing looks
// identical to a working one, so a write is read back through a DIFFERENT tool wherever one exists.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/easydev-org/easybus-sdk/agent"
	"github.com/easydev-org/easybus-sdk/transport/nats"
)

// The state a run accumulates, so later steps can use what earlier ones made.
type state struct {
	Repo    string // owner/name on GitHub
	Mirror  string // owner/name in the forge
	Branch  string
	// File is a path the listing found, so the read step opens something that exists rather than a
	// name this program assumed.
	File    string
	// PatchFile is the file this run writes. Unique per run, so the suite passes on a repository that
	// already has a previous run's file on main.
	PatchFile string
	Commit  string
	PR      int
	Issue   int
	RunID   int64
	JobID   int64
	Tag     string
	Release string
	// Scratch is the throwaway repository the destructive tools work in, so they never touch the one
	// the rest of the suite depends on.
	Scratch        string
	ScratchRenamed string
}

func main() {
	var (
		natsURL  = flag.String("nats", envOr("NATS_URL", "nats://127.0.0.1:4222"), "NATS url")
		tenant   = flag.String("tenant", envOr("E2E_TENANT", "alice"), "tenant")
		session  = flag.String("session", envOr("E2E_SESSION", "e2e-all"), "session")
		extID    = flag.String("ext", envOr("E2E_EXT", "gitdev"), "extension id")
		repo     = flag.String("repo", os.Getenv("GH_E2E_REPO"), "GitHub owner/name to work in")
		mirror   = flag.String("mirror", os.Getenv("GH_E2E_MIRROR"), "forge owner/name to work in")
		// A repository that HAS workflows. The CI tools are read-only, so pointing them at one with a
		// history is safe and is the difference between verifying them and skipping them.
		ciRepo   = flag.String("ci-repo", envOr("GH_E2E_CI_REPO", os.Getenv("GH_E2E_REPO")), "repository with CI runs")
		// The mirror's scheme and host. A remote is an ADDRESS: the extension picks which configured
		// server a call belongs to by parsing its host, so owner/name alone cannot be routed.
		forge    = flag.String("forge", envOr("GH_E2E_FORGE", "http://forgejo.dev.svc.cluster.local"), "mirror scheme+host")
		keep     = flag.Bool("keep", false, "leave the created branch, tag and PR behind")
		only     = flag.String("only", "", "run only the named step (for debugging)")
		verbose  = flag.Bool("v", false, "print every reply")
	)
	flag.Parse()

	if *repo == "" {
		fatalf("GH_E2E_REPO is required (owner/name on GitHub)")
	}
	if *mirror == "" {
		// The forge account is not the upstream owner — a mirror of easydev-org/x lives at
		// agentdev/x — so it cannot be derived from -repo and is not guessed.
		fatalf("GH_E2E_MIRROR is required (owner/name in the forge)")
	}

	b, err := nats.Connect(*natsURL)
	if err != nil {
		fatalf("connect to %s: %v", *natsURL, err)
	}
	defer func() { _ = b.Close() }()
	a := agent.New(b)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	if _, err := a.Discover(ctx, 15000); err != nil {
		fatalf("discover extensions: %v", err)
	}

	r := &runner{
		ctx: ctx, a: a, extID: *extID, tenant: *tenant, session: *session,
		repo: *repo, ciRepo: *ciRepo, mirror: *mirror, forgeBase: strings.TrimRight(*forge, "/"),
		only: *only, verbose: *verbose,
	}
	st := &state{Repo: *repo, Mirror: *mirror}

	// ORDERED BECAUSE THE TOOLS FORM A WORKFLOW. Grouped by what they need to exist first.
	steps := []struct {
		name string
		fn   func(*state) error
	}{
		// Every read asserts a NON-EMPTY answer, not merely an absent error: a tool that returns
		// nothing looks exactly like one that works.
		{"gh-whoami", r.stepWhoami},
		{"gh-orgs", r.stepOrgs},
		{"gh-repos", r.stepRepos},
		{"gh-info", r.stepRepoInfo},

		// The mirror: read, then write, then read the write back through another tool.
		{"git-file-list", r.stepFileList},
		{"git-file-read", r.stepFileRead},
		{"git-sync-upstream", r.stepMirrorSync},
		{"git-branch-create", r.stepBranchCreate},
		{"git-file-patch", r.stepFilePatch},
		{"git-file-list (after patch)", r.stepVerifyPatch},
		{"git-push-branch", r.stepPushBranch},

		// GitHub, on what the mirror produced.
		{"gh-pr-create", r.stepPRCreate},
		{"gh-pr-files", r.stepPRFiles},
		{"gh-pr-diff", r.stepPRDiff},
		{"gh-pr-checks", r.stepPRChecks},
		{"gh-pr-merge", r.stepPRMerge},

		// Issues and comments.
		{"gh-issue-create", r.stepIssueCreate},
		{"gh-issue-show", r.stepIssueShow},
		{"gh-issue-comment", r.stepIssueComment},
		{"gh-issue-comments", r.stepIssueComments},
		{"gh-issue-list", r.stepIssueList},
		{"gh-issue-close", r.stepIssueClose},

		// Tags and releases.
		{"gh-tag-create", r.stepTagCreate},
		{"gh-tag-show", r.stepTagShow},
		{"gh-tag-list", r.stepTagList},
		{"gh-releases", r.stepReleases},

		// CI: the run the push started.
		{"gh-workflow-list", r.stepWorkflowList},
		{"gh-run-list", r.stepRunList},
		{"gh-run-jobs", r.stepRunJobs},
		{"gh-run-log", r.stepRunLog},
		{"gh-job-log", r.stepJobLog},
		{"gh-check-log", r.stepCheckLog},
		{"gh-run-cancel", r.stepRunCancel},
		{"gh-run-rerun", r.stepRunRerun},
		{"gh-run-dispatch", r.stepRunDispatch},

		// Artifacts and packages: read-only, and may legitimately be empty.
		{"gh-artifacts", r.stepArtifacts},
		{"gh-artifact-download", r.stepArtifactDownload},
		{"gh-packages", r.stepPackages},

		// Destructive, last, and each cleaned up by the one after it.
		{"gh-repo-create", r.stepRepoCreate},
		{"gh-repo-rename", r.stepRepoRename},
		{"gh-tag-delete", r.stepTagDelete},
		{"gh-repo-delete", r.stepRepoDelete},
	}

	r.steps = steps
	if !*keep {
		defer r.cleanup(st)
	}
	for _, s := range steps {
		if *only != "" && !strings.Contains(s.name, *only) {
			continue
		}
		if err := s.fn(st); err != nil {
			r.fail(s.name, err)
		}
	}
	r.summary()
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatalf(f string, a ...any) {
	fmt.Fprintf(os.Stderr, "e2e-all: "+f+"\n", a...)
	os.Exit(2)
}
