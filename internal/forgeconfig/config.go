// Package forgeconfig parses the mirror (Gitea/Forgejo) config knobs into a strongly-typed server
// list. The config surface is intentionally small:
//
//   - `servers` (secret, JSON): a list of {host, kind, token, user?} — the
//     credential for each git host. It is a SECRET (the agent withholds its
//     value from clients; the UI shows set/not-set only).
//   - `git-protocol` (enum): https | ssh — the transport preference (ssh is
//     not yet implemented; the enum is reserved).
//   - `default-branch` (string): branch used when a remote has no HEAD hint.
//
// A server is selected by the remote URL's host; an unknown host falls back to
// anonymous access (public repos) unless `require-auth` is set.
package forgeconfig

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Knob names — the single source of truth for the manifest + readers.
const (
	KnobServers       = "servers"
	// KnobToken is the GITHUB credential — the UPSTREAM, not the mirror. It is a separate knob
	// because a deployment writes to one host and reads from another, and the two have different
	// credentials.
	KnobToken         = "token"
	KnobGitProtocol   = "git-protocol"
	KnobDefaultBranch = "default-branch"
	KnobRequireAuth   = "require-auth"
	// KnobSandboxes is the sandbox list (shared with sandbox-ext). Every git-*
	// tool runs IN a sandbox, so this is required alongside `servers`.
	KnobSandboxes = "sandboxes"
)

// Sandbox is one configured easyworker (the machine git runs on).
type Sandbox struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	Token string `json:"token"`
}

// ParseSandboxes parses the `sandboxes` JSON list, dropping invalid entries.
func ParseSandboxes(raw string) []Sandbox {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var rows []Sandbox
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil
	}
	out := make([]Sandbox, 0, len(rows))
	for _, s := range rows {
		s.Name = strings.TrimSpace(s.Name)
		s.URL = strings.TrimSpace(s.URL)
		s.Token = strings.TrimSpace(s.Token)
		if s.Name == "" || s.URL == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// SelectSandbox resolves a sandbox by name (” = the first configured).
func SelectSandbox(list []Sandbox, wanted string) (Sandbox, error) {
	if len(list) == 0 {
		return Sandbox{}, fmt.Errorf("no sandboxes configured (set the `%s` config)", KnobSandboxes)
	}
	if wanted == "" {
		return list[0], nil
	}
	for _, s := range list {
		if s.Name == wanted {
			return s, nil
		}
	}
	names := make([]string, 0, len(list))
	for _, s := range list {
		names = append(names, s.Name)
	}
	return Sandbox{}, fmt.Errorf("unknown sandbox %q (known: %s)", wanted, strings.Join(names, ", "))
}

// ServerKind is the forge a host belongs to.
//
// ONLY Gitea — and Forgejo, which implements the Gitea API — is supported. GitHub
// and GitLab were removed on purpose: one forge verified against a real server
// beats three half-verified API shapes. A `generic` host still works for
// clone/push (plain git over HTTPS), but NOT for the read-only remote tools,
// which need the platform API.
type ServerKind string

const (
	KindGitea   ServerKind = "gitea"
	KindGeneric ServerKind = "generic"
)

// Server is one configured git host.
type Server struct {
	// Host is the hostname (no scheme, no path), e.g. "github.com".
	Host string `json:"host"`
	// Kind selects the auth convention. Empty defaults to "generic".
	Kind ServerKind `json:"kind"`
	// Token is the PAT. Empty = anonymous.
	Token string `json:"token"`
	// User overrides the basic-auth username (Gitea needs the real account).
	User string `json:"user"`
	// APIURL overrides the platform API base (no trailing slash). Empty = derive
	// it from Host and Kind. Needed when a self-hosted forge serves its API from
	// a different origin than the git host, or under a sub-path.
	APIURL string `json:"api_url,omitempty"`
}

// APIBase returns the platform API base for this server (no trailing slash).
//
// The read-only remote tools talk to the FORGE's HTTP API rather than cloning,
// because reading one file out of a large repo should not download the repo.
// Each kind exposes a different base:
//
//	github  https://api.github.com
//	gitlab  https://<host>/api/v4
//	gitea   https://<host>/api/v1
//
// `generic` has no comparable API, so APIBase returns "" and the caller reports
// that remote reads are unsupported for that host (rather than guessing a URL
// that would 404).
func (s Server) APIBase() string {
	if s.APIURL != "" {
		return strings.TrimRight(s.APIURL, "/")
	}
	switch s.Kind {
	case KindGitea:
		return "https://" + s.Host + "/api/v1"
	default:
		return ""
	}
}

// Config is the parsed, validated gitea-ext configuration.
type Config struct {
	// Sandboxes is the set of easyworker sandboxes git runs on (required).
	Sandboxes     []Sandbox
	Servers       []Server
	GitProtocol   string // "https" (default) | "ssh"
	DefaultBranch string // "main" (default)
	RequireAuth   bool
	// UpstreamToken authenticates against a host that is NOT in `servers`.
	//
	// WHY IT IS SEPARATE FROM `Servers`: the mirror is a local forge the extension writes to, and the
	// upstream is GitHub, which it only READS (and pushes branches to). Deployments configure those
	// with different knobs — `servers` for the mirror, `token` for GitHub — so looking a GitHub URL up
	// in `servers` finds nothing and the push goes out anonymous. That is what happened: a branch
	// reached the forge and was refused by GitHub with "no anonymous write access".
	UpstreamToken string
}

// Parse builds a Config from the raw knob values (as read from extension
// config). A malformed `servers` value yields an empty list (the tools then
// report a clear "not configured" error rather than panicking).
func Parse(get func(name string) string) (Config, error) {
	servers, err := ParseServers(get(KnobServers))
	if err != nil {
		return Config{}, err
	}
	c := Config{
		Sandboxes:     ParseSandboxes(get(KnobSandboxes)),
		Servers:       servers,
		UpstreamToken: strings.TrimSpace(get(KnobToken)),
		GitProtocol:   orDefault(get(KnobGitProtocol), "https"),
		DefaultBranch: orDefault(get(KnobDefaultBranch), "main"),
		RequireAuth:   get(KnobRequireAuth) == "true",
	}
	switch c.GitProtocol {
	case "https", "ssh":
	default:
		return Config{}, fmt.Errorf("config: git-protocol %q: want https or ssh", c.GitProtocol)
	}
	return c, nil
}

// ParseServers parses the `servers` JSON list, dropping invalid entries (a bad
// row must never take down the whole extension). Entries without a host are
// dropped; an empty token is allowed (anonymous / public repos).
func ParseServers(raw string) ([]Server, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var rows []Server
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		return nil, fmt.Errorf("config: servers is not a JSON list: %w", err)
	}
	out := make([]Server, 0, len(rows))
	seen := map[string]bool{}
	for _, s := range rows {
		host := normalizeHost(s.Host)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		if s.Kind == "" {
			s.Kind = KindGeneric
		}
		s.Host = host
		s.Token = strings.TrimSpace(s.Token)
		s.User = strings.TrimSpace(s.User)
		out = append(out, s)
	}
	return out, nil
}

// normalizeHost extracts a bare hostname from a host string that may carry a
// scheme or path ("https://github.com/x" -> "github.com").
func normalizeHost(h string) string {
	h = strings.TrimSpace(strings.ToLower(h))
	if h == "" {
		return ""
	}
	if strings.Contains(h, "://") {
		if u, err := url.Parse(h); err == nil && u.Host != "" {
			h = u.Host
		}
	}
	if i := strings.IndexByte(h, '/'); i >= 0 {
		h = h[:i]
	}
	// Strip a :port (host matching is by name, not port).
	if i := strings.LastIndexByte(h, ':'); i >= 0 {
		// Keep IPv6 literals intact ([::1]).
		if !strings.Contains(h[i:], "]") {
			h = h[:i]
		}
	}
	return strings.TrimSuffix(h, ".")
}

// ServerForHost returns the configured server for a hostname, or (zero, false)
// when none matches.
func (c Config) ServerForHost(host string) (Server, bool) {
	host = normalizeHost(host)
	for _, s := range c.Servers {
		if s.Host == host {
			return s, true
		}
	}
	return Server{}, false
}

// HostOf extracts the hostname from a git remote URL (https, ssh://, or
// scp-style git@host:path). Returns "" when it cannot be determined.
func HostOf(remote string) string {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return ""
	}
	if strings.Contains(remote, "://") {
		if u, err := url.Parse(remote); err == nil {
			return normalizeHost(u.Host)
		}
		return ""
	}
	// scp-style: [user@]host:path
	if at := strings.IndexByte(remote, '@'); at >= 0 {
		rest := remote[at+1:]
		if colon := strings.IndexByte(rest, ':'); colon > 0 {
			return normalizeHost(rest[:colon])
		}
	}
	return ""
}

// AuthUser is the HTTPS basic-auth username for a server. GitHub accepts
// `x-access-token`, GitLab `oauth2`, Gitea the real account name; a `user`
// override always wins.
func (s Server) AuthUser() string {
	if s.User != "" {
		return s.User
	}
	// The basic-auth USER for a PAT.
	//
	// It has to be the REAL account name. Both placeholders that "everyone knows"
	// work do NOT work on Gitea: it looks the username up and, when it is not a
	// real account, logs
	//
	//     Failed to verify user: user does not exist [uid: 0, name: x-access-token]
	//
	// and hands the git client the barest possible
	//
	//     remote: Verify
	//
	// "token" and "x-access-token" were both tried against a live Gitea 1.22 and
	// both failed; Forgejo happened to accept one, which made the bug look
	// environment-specific. Configure `user` on the server entry — see
	// docs: `servers[].user`.
	//
	// Returning "" makes the caller fall back to the TOKEN as the username, which
	// Gitea does accept.
	return ""
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}
