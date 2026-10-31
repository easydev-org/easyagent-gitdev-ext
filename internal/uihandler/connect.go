package uihandler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"connectrpc.com/connect/v2"

	"github.com/easydev-org/easyagent-gitdev-ext/gen/ui/v1/uiv1connect"
)

/**
 * The UI API, as a Connect service.
 *
 * THIS REPLACES twenty-two hand-written REST routes. The routes worked; what they did not do is state
 * their contract. Their response shapes were `map[string]any` literals written at the point of
 * emission, so the panel's contract was a side effect of whatever the handler happened to emit — and
 * twice it disagreed with what the panel read (`Path` vs `path`, `Name` vs `name`), which rendered an
 * EMPTY file tree against data that was correct and present.
 *
 * Now the contract is `proto/ui/v1/ui.proto`, the panel is generated from it, and a mismatch is a
 * compile error on both sides instead of an empty page.
 *
 * WHAT DID NOT CHANGE, deliberately: every behaviour the routes encoded. A forge that cannot be
 * reached is UNAVAILABLE rather than UNAUTHENTICATED, so a network blip does not log the reader out. A
 * job log that is not published yet is a distinct code the panel can act on. Those are comments-to-code
 * translations, and losing one would be the kind of regression that shows up as "the panel is broken"
 * with no clue why.
 */
type ConnectService struct {
	// Identity resolves the caller's bearer to a tenant. REQUIRED: without it every request is
	// refused, because the alternative is trusting a tenant the caller supplied.
	Identity *IdentityResolver
	Pulls    PullSource
	Issues   IssueSource
	Runs     RunSource
	Browse   BrowseSource
	Mirrored MirroredSource
	Forge    MirrorForgeFunc
}

var _ uiv1connect.UiServiceHandler = (*ConnectService)(nil)

/**
 * tenantFromCtx reads the tenant the AUTH INTERCEPTOR resolved.
 *
 * THE TENANT IS NEVER TAKEN FROM THE REQUEST. It comes from the bearer, because a tenant in a query
 * parameter or a body is one a caller can choose — `?tenant=victim` was a real hole here once.
 *
 * It is on the context because the sources below take a plain context and must resolve the tenant's
 * token without a second round trip to the agent.
 */
func tenantFromCtx(ctx context.Context) (string, error) {
	t, ok := ctx.Value(tenantKey{}).(string)
	if !ok || t == "" {
		// The interceptor runs before every method, so reaching here means the interceptor was not
		// installed. Failing closed is the only safe reading of that.
		return "", connect.NewError(connect.CodeInternal, "no tenant on the request context")
	}
	return t, nil
}

/**
 * AuthInterceptor resolves the caller's tenant and puts it on the context.
 *
 * ONE PLACE, NOT TWENTY-TWO. Every route used to call `who()` itself, which meant every route could
 * forget to — and the failure mode of forgetting is serving another tenant's data. An interceptor
 * cannot be forgotten: it wraps the whole service.
 *
 * A NETWORK FAULT IS NOT A BAD CREDENTIAL. If the agent cannot be reached the answer is UNAVAILABLE,
 * because reporting UNAUTHENTICATED would log the reader out over a blip and they would not know why.
 */

// need reports the source is nil as an actionable error rather than a panic.
func need(configured bool, what string) error {
	if configured {
		return nil
	}
	return connect.NewError(connect.CodeFailedPrecondition,
		fmt.Sprintf("%s are not configured in this deployment", what))
}

// requireRepo validates the `owner/name` a request names.
func requireRepo(repo string) error {
	if strings.TrimSpace(repo) == "" {
		return connect.NewError(connect.CodeInvalidArgument, "repo (owner/name) is required")
	}
	return nil
}

// requirePositive validates a numeric id the request names.
func requirePositive(name string, id int64) error {
	if id <= 0 {
		return connect.NewError(connect.CodeInvalidArgument, fmt.Sprintf("%s is required", name))
	}
	return nil
}

/**
 * forgeErr maps a forge failure to a code a browser can act on.
 *
 * A forge that is merely UNREACHABLE is UNAVAILABLE, not INTERNAL: it is a state of a remote
 * dependency, and a panel retries it rather than showing a permanent failure. A missing repository is
 * NOT_FOUND and a refused read is PERMISSION_DENIED, so the reader can tell "wrong path" from "no
 * access" without reading the message.
 */
func forgeErr(err error) error {
	msg := err.Error()
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "not found") || strings.Contains(low, "404"):
		return connect.NewError(connect.CodeNotFound, msg)
	case strings.Contains(low, "unauthorized") || strings.Contains(low, "401") ||
		strings.Contains(low, "forbidden") || strings.Contains(low, "403"):
		return connect.NewError(connect.CodePermissionDenied, msg)
	default:
		return connect.NewError(connect.CodeUnavailable, msg)
	}
}

// internal is the shape most handlers use for an unexpected failure.
func internal(what string, err error) error {
	return connect.NewError(connect.CodeInternal, fmt.Sprintf("%s: %v", what, err))
}

/**
 * NewServer builds the Connect server for this service, with authentication installed.
 *
 * A SERVER-LEVEL INTERCEPTOR, NOT TWENTY-TWO CHECKS. Every route used to call `who()` itself, which
 * meant every route could forget to — and the failure mode of forgetting is serving another tenant's
 * data. connect v2 expresses this as `ServerInterceptor`: it wraps the whole service, so no method can
 * be added later that skips it.
 */
func (s *ConnectService) NewServer() *connect.Server {
	server := connect.NewServer(s.authInterceptor())
	uiv1connect.RegisterUiServiceHandler(server, s)
	return server
}

/**
 * authInterceptor resolves the caller's tenant and puts it on the context.
 *
 * THE TENANT IS NEVER TAKEN FROM THE REQUEST. It comes from the bearer, because a tenant in a query
 * parameter or a body is one a caller can choose — `?tenant=victim` was a real hole here once.
 *
 * A NETWORK FAULT IS NOT A BAD CREDENTIAL: if the agent cannot be reached the answer is UNAVAILABLE,
 * because reporting UNAUTHENTICATED would log the reader out over a blip and they would not know why.
 */
func (s *ConnectService) authInterceptor() connect.ServerInterceptor {
	return func(next connect.ServerFunc) connect.ServerFunc {
		return func(ctx context.Context, spec connect.Spec, stream connect.ServerStream) error {
			if s.Identity == nil {
				// Failing closed: a deployment that forgot to wire identity must not serve data.
				return connect.NewError(connect.CodeInternal, "identity is not configured")
			}
			// The per-RPC CallInfo is on the context, put there by Server.Call before any handler
			// runs. It carries the request headers.
			info, ok := connect.CallInfoForServerContext(ctx)
			if !ok {
				return connect.NewError(connect.CodeInternal, "no call info on the request context")
			}
			v := info.RequestHeader().Get("Authorization")
			bearer := ""
			if len(v) > 7 && strings.EqualFold(v[:7], "bearer ") {
				bearer = strings.TrimSpace(v[7:])
			}
			tenant, terr := s.Identity.Tenant(ctx, bearer)
			if terr != nil {
				if errors.Is(terr, ErrUnauthenticated) {
					return connect.NewError(connect.CodeUnauthenticated, terr.Error())
				}
				return connect.NewError(connect.CodeUnavailable,
					fmt.Sprintf("cannot resolve the caller: %v", terr))
			}
			return next(context.WithValue(ctx, tenantKey{}, tenant), spec, stream)
		}
	}
}
