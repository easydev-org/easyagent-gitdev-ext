// Package uihandler serves the browser panel's data: the MIRROR's files and the UPSTREAM's pull
// requests.
//
// TWO SOURCES, ONE PANEL, because they are two halves of one job. The mirror is where a change is
// made, so its files are what a reader wants while writing one; the pull request is where the change
// is reviewed, so that is what they want afterwards. A panel that showed only one of them would
// send the reader to a second extension mid-task.
//
// NOTHING HERE WRITES. Browsing and reviewing are reads; every change goes through a tool, where the
// permission model already lives. A page that could merge would need its own permission story and
// does not have one.
package uihandler

import (
	"context"
	"errors"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

// MirrorForgeFunc builds a forge client for one MIRROR repository in one caller's scope.
//
// A FUNCTION rather than a client because the token and the repository come from extension CONFIG,
// which can change while this process runs, and because the client is per-tenant.
type MirrorForgeFunc func(ctx context.Context, session, tenant, repo string) (*forge.Client, error)

// tenantKey carries the resolved tenant through a request's context, so a PullSource — which takes a
// plain context — can resolve the token without re-authenticating. The pull handlers put it there
// after `who()` has succeeded, and nothing else writes it: a value a caller could set would be a
// value a caller could choose.
type tenantKey struct{}

// tenantFrom reads the tenant the handler resolved.
func tenantFrom(ctx context.Context) (string, error) {
	if t, ok := ctx.Value(tenantKey{}).(string); ok && t != "" {
		return t, nil
	}
	return "", errors.New("no tenant on the request context")
}
