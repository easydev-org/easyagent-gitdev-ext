package tools

import (
	"fmt"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forgeconfig"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
)

// These two tools read a REMOTE repository over the forge's HTTP API, so they
// need no sandbox and no clone — unlike every other git-* tool here.
//
// Why that matters: answering "what does the README say" or "which branches
// exist" used to require a clone into a sandbox first, which for a large repo is
// minutes of transfer to read a few hundred bytes. These answer in one request.

// remoteClient builds a forge client for the `remote` argument.
//
// The token comes from the matching entry in `servers`; an unconfigured host is
// tried anonymously (public repositories). The API base is derived from the
// server's kind/host, so no extra configuration is needed for a normal install.
func (t *toolset) remoteClient(session, tenant, remote string) (*forge.Client, forgeconfig.Config, error) {
	cfg := t.forgeCfg(session, tenant)
	host := forgeconfig.HostOf(remote)
	if host == "" {
		return nil, cfg, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: fmt.Sprintf("cannot parse a host from remote %q", remote),
		}
	}
	srv, ok := cfg.ServerForHost(host)
	if !ok {
		// No entry for this host: only a public repository can be read, and the
		// forge will say so. Failing here instead would be wrong — an anonymous
		// read of a public repo is a legitimate, common case.
		srv = forgeconfig.Server{Host: host, Kind: forgeconfig.KindGitea}
	}
	base := srv.APIBase()
	if base == "" {
		return nil, cfg, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: fmt.Sprintf("%s is not configured as a Gitea/Forgejo host (set `kind` in the `servers` config), or use git-clone", host),
		}
	}
	cl, err := forge.New(base, srv.Token, remote)
	if err != nil {
		return nil, cfg, &extension.TypedError{
			Code:    easybus.ToolResultErrorCodeInvalidArgument,
			Message: err.Error(),
		}
	}
	return cl, cfg, nil
}

// forgeCfg reads the tenant's mirror (Gitea/Forgejo) server list from configuration.
//
// A SEPARATE knob from the GitHub token: the mirror is a different host with a different credential,
// and one being absent must not look like the other being broken. An empty config is valid — it is
// how a forge client reads a public repository anonymously.
func (t *toolset) forgeCfg(session, tenant string) forgeconfig.Config {
	if t.deps.GetConfig == nil {
		return forgeconfig.Config{}
	}
	// Parse, not just the server list: this Config is what the upstream calls read their CREDENTIAL
	// from, and the GitHub token lives under `token` rather than in `servers`. Building the struct by
	// hand here left UpstreamToken empty, so every push to an upstream went out anonymous — a bug
	// that only shows up when the mirror and the upstream are different hosts, which is the normal
	// deployment.
	cfg, err := forgeconfig.Parse(func(name string) string {
		raw, _ := t.deps.GetConfig(name, session, tenant).(string)
		return raw
	})
	if err != nil {
		// A malformed value is reported by the tools that need it, not here: this function is also
		// used to decide whether the mirror is configured at all.
		return forgeconfig.Config{}
	}
	return cfg
}
