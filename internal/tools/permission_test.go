package tools

import (
	"context"
	"testing"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
)

func ctxWith(gs ...*easybus.Grant) context.Context {
	return extension.WithGrants(context.Background(), gs)
}

func repoGrant(key, repo string) *easybus.Grant {
	return &easybus.Grant{Key: key, Params: easybus.Struct(map[string]any{"repo": repo})}
}

// Reading, opening an issue and commenting need NO grant. A session that cannot ask a question is
// not a safer session, only a quieter one.
func TestExpressingNeedsNoPermission(t *testing.T) {
	m := RequireMap()
	for _, tool := range []string{
		"gh-issue-create", "gh-issue-comment", "gh-issue-list", "gh-issue-show",
		"gh-issue-comments", "gh-pr-files", "gh-pr-diff", "gh-pr-checks",
	} {
		if got := m[tool]; len(got) != 0 {
			t.Errorf("%s should need no permission, but requires %v", tool, got)
		}
	}
}

// Everything that CHANGES something needs repo.maintainer — including closing an issue, which a
// contributor may not do even to its own.
/**
 * THE GRANT FOLLOWS WHOSE WORK THE TOOL TOUCHES, not how much it changes.
 *
 * Acting on your own pull request — opening it, closing it — needs only `repo.write`. Acting on someone
 * else's needs `repo.maintainer`, because closing their proposal or merging into a branch they build on
 * reaches a person who did not ask for it.
 *
 * THE PAIR THAT MAKES THIS TEST WORTH WRITING is gh-pr-close vs gh-pr-manage: the same underlying
 * operation at two levels. If a later change collapsed them back into one tool, this is where the loss of
 * distinction would surface.
 */
func TestOwnWorkNeedsWriteAndOthersNeedMaintainer(t *testing.T) {
	m := RequireMap()
	needsWrite := []string{"gh-issue-close", "gh-pr-create", "gh-pr-close"}
	needsMaintainer := []string{
		"gh-pr-manage",
		"gh-run-dispatch", "gh-run-rerun", "gh-run-cancel",
		"gh-tag-create", "gh-tag-delete",
		"gh-repo-rename", "gh-repo-delete",
	}
	for _, tool := range needsWrite {
		if got := m[tool]; len(got) != 1 || got[0] != PermWrite {
			t.Errorf("%s requires %v, want [%s]: it acts only on the caller's own work",
				tool, got, PermWrite)
		}
	}
	for _, tool := range needsMaintainer {
		if got := m[tool]; len(got) != 1 || got[0] != PermMaintainer {
			t.Errorf("%s requires %v, want [%s]", tool, got, PermMaintainer)
		}
	}

	// AND NOTHING ASKS FOR BOTH. A tool requiring write and maintainer would mean the write grant is
	// never sufficient, which is the distinction this split exists to make real.
	for tool, keys := range m {
		if len(keys) > 1 {
			t.Errorf("%s requires %v; a tool should need one grant, not a combination", tool, keys)
		}
	}
}

// Closing a pull request you opened and closing someone else's are different tools, and the weaker one
// must not be usable to reach the stronger action.
func TestPRCloseIsNotPRManage(t *testing.T) {
	m := RequireMap()
	if m["gh-pr-close"] == nil || m["gh-pr-manage"] == nil {
		t.Fatal("both pull-request close paths must be declared")
	}
	if m["gh-pr-close"][0] == m["gh-pr-manage"][0] {
		t.Error("gh-pr-close and gh-pr-manage share a grant; the split would be cosmetic")
	}
}

// A maintainer grant is scoped to one repository and does not leak to another.
func TestMaintainerIsScopedToOneRepository(t *testing.T) {
	ctx := ctxWith(repoGrant(PermMaintainer, "o/one"))
	ts := &toolset{}
	if err := ts.requireRepo(ctx, PermMaintainer, "o/one"); err != nil {
		t.Errorf("the granted repository should pass: %v", err)
	}
	if err := ts.requireRepo(ctx, PermMaintainer, "o/two"); err == nil {
		t.Error("a grant for o/one must not cover o/two")
	}
}

// A wildcard covers one owner's repositories, which is how "all my repositories" is expressed.
func TestWildcardCoversOwner(t *testing.T) {
	ctx := ctxWith(repoGrant(PermMaintainer, "o/*"))
	ts := &toolset{}
	if err := ts.requireRepo(ctx, PermMaintainer, "o/anything"); err != nil {
		t.Errorf("o/* should cover o/anything: %v", err)
	}
	if err := ts.requireRepo(ctx, PermMaintainer, "other/anything"); err == nil {
		t.Error("o/* must not cover another owner")
	}
}

// Creating a repository is an ORGANIZATION decision: there is no repository yet to scope a grant
// to, so a repository-level grant must not authorise it.
func TestRepoCreateIsOrgScoped(t *testing.T) {
	const slug = "acme/new-thing"

	ctx := ctxWith(repoGrant(PermMaintainer, slug))
	if err := (&toolset{}).requireRepo(ctx, PermOrgAdmin, slug); err == nil {
		t.Error("repo.maintainer must not authorise creating a repository")
	}

	ctx2 := ctxWith(&easybus.Grant{
		Key: PermOrgAdmin, Params: easybus.Struct(map[string]any{"org": "acme"}),
	})
	if err := (&toolset{}).requireRepo(ctx2, PermOrgAdmin, slug); err != nil {
		t.Errorf("org.admin should authorise creation under that org: %v", err)
	}

	// And not under a different org.
	ctx3 := ctxWith(&easybus.Grant{
		Key: PermOrgAdmin, Params: easybus.Struct(map[string]any{"org": "other"}),
	})
	if err := (&toolset{}).requireRepo(ctx3, PermOrgAdmin, slug); err == nil {
		t.Error("org.admin for 'other' must not authorise creating under 'acme'")
	}
}

// There are exactly two keys. A third would mean the model crept back toward per-operation grants.
func TestExactlyTwoPermissions(t *testing.T) {
	if PermMaintainer == PermOrgAdmin {
		t.Fatal("the two permission keys must differ")
	}
}
