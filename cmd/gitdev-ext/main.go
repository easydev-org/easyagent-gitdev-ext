// git-ext serves the mirror and GitHub: change the code in the mirror, push the branch, then open
// and land a pull request. There is no local git and no sandbox — the forge's API is the workspace.
package main

import (
	"connectrpc.com/connect/v2/connecthttp"
	"context"
	"embed"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	easybus "github.com/easydev-org/easybus-sdk"
	"github.com/easydev-org/easybus-sdk/extension"
	"github.com/easydev-org/easybus-sdk/job"
	"github.com/easydev-org/easybus-sdk/manifest"
	"github.com/easydev-org/easybus-sdk/transport/nats"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/forge"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/forgeconfig"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/tools"
	"github.com/easydev-org/easyagent-gitdev-ext/internal/uihandler"
	// The PR source reads GitHub for the UI's pull-request pages — the one remote read in the UI's
	// data path. Everything else the panel used to show came from a local checkout, which this
	// extension no longer keeps.
	"github.com/easydev-org/easyagent-gitdev-ext/internal/ghclient"
)

//go:embed manifest.yaml
var manifestYAML []byte

// uiFiles is the browser UI, embedded so the binary is self-contained: no asset directory to ship
// alongside the image, and no chance of the UI and the API it calls drifting apart in a deployment.
//
// THE SOURCE IS ELSEWHERE NOW. The panel is easydev-org/easyagent-gitdev-ext-ui, built by that
// repository's CI; ui/fetch-ui.sh brings the built bundle here and this embeds it. The reason for
// still embedding it is the sentence above: two images could come up at different versions, and that
// is the class of bug the split exists to remove, not to reintroduce one layer up.
//
//go:generate sh ./fetch-ui.sh
//go:embed all:ui
var uiFiles embed.FS

// uiBase is where this extension's UI — and therefore its API — is mounted.
//
// ONE constant for three consumers that must agree: UISpec.Base (what the static handler claims),
// the API routes (what the JSON handlers claim), and the host's reverse-proxy rule. When they
// disagreed, an API path fell through to the SPA and answered with an HTML document; the browser's
// JSON parse failed and no 404 pointed at the path that was wrong.
const uiBase = "/ext/gitdev"

// uiMux builds the extension's HTTP surface: the health/version probes plus the UI's data API.
//
// The probe routes are re-declared here because supplying a Handler REPLACES the SDK's default one;
// without them a deployment that added a UI would lose its liveness endpoints and start crash
// looping under a probe that had worked before.
func uiMux(ver, agentURL string, pulls uihandler.PullSource, issues uihandler.IssueSource, runs uihandler.RunSource, browse uihandler.BrowseSource, mirrored uihandler.MirroredSource, forgeFor uihandler.MirrorForgeFunc) http.Handler {
	mux := http.NewServeMux()
	// THE API LIVES UNDER uiBase, NOT AT THE ROOT.
	//
	// MountUI serves the SPA for everything under Base except exactly `Base + APIPrefix`. So
	// "/api/v1/health" at the root is outside Base entirely — it reaches this mux untouched and answers
	// fine — while "/ext/gitdev/api/v1/health" falls into the SPA fallback and comes back as
	// index.html. The first version registered the probes at the root and they 404d for every caller
	// that went through the mount, which is every caller.
	apiRoot := uiBase + "/api"
	mux.HandleFunc(apiRoot+"/v1/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"name":"gitdev"}`))
	})
	mux.HandleFunc(apiRoot+"/v1/version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gitdev","version":"` + ver + `"}`))
	})
	// Identity resolves a caller's bearer to a tenant by asking the agent, which owns the token
	// table. Without this the UI API would have to trust `?tenant=`, i.e. let anyone read any
	// tenant's data by naming it.
	//
	// THE API IS A CONNECT SERVICE NOW, not a set of JSON routes. Its contract lives in
	// proto/ui/v1/ui.proto, the panel is generated from that same file, and a field the panel reads
	// but the server does not send is a compile error rather than an empty page. The old routes twice
	// disagreed with what the panel read (`Path` vs `path`), which rendered an empty file tree against
	// data that was correct and present.
	svc := &uihandler.ConnectService{
		Identity: &uihandler.IdentityResolver{AgentURL: agentURL},
		// Pull requests are the one remote read the UI makes: a page shows the CURRENT state, where a
		// card replays the snapshot a tool returned.
		Pulls:    pulls,
		Issues:   issues,
		Runs:     runs,
		Browse:   browse,
		Mirrored: mirrored,
		// The MIRROR's pages. Same shape as Pulls: a function resolved per request, because the
		// token and the server list live in configuration and can change while this pod runs.
		Forge: forgeFor,
	}
	// THE CONNECT SERVICE LIVES UNDER THE SAME apiRoot, so MountUI's single APIPrefix covers it.
	//
	// connecthttp.Mount registers each procedure at its ABSOLUTE path — "/ui.v1.UiService/GetWhoami"
	// — with no prefix of its own. So the mux serves apiRoot + "/ui.v1.UiService/…" and STRIPS apiRoot
	// before handing the request to Connect, which then sees the path it expects.
	//
	// WITHOUT THIS PREFIX the procedure paths have no file extension, so the SPA fallback answers them
	// with index.html and the client fails parsing HTML as protobuf, with no failing URL to point at.
	// The alternative — a second APIPrefix in the SDK — would change every extension for one
	// extension's routing.
	api := http.NewServeMux()
	connecthttp.Mount(api, svc.NewServer(), connecthttp.WithReadMaxBytes(0))
	mux.Handle(apiRoot+"/", http.StripPrefix(apiRoot, api))
	return mux
}

/**
 * mirrorForge builds the function the panel uses to read the MIRROR.
 *
 * THE TENANT ARGUMENTS ARE IN DIFFERENT ORDERS, and getting it wrong would be invisible: the panel's
 * callback takes (session, tenant, repo) while Extension.GetConfig takes (tenant, name, session). A
 * swap would still return a config value — the wrong tenant's — so the mix-up is written out here
 * rather than left to a call site to remember.
 *
 * A missing server entry falls through to New with no token, which reads a PUBLIC repository
 * anonymously. That is a legitimate case, so it is not treated as a failure here.
 */
func mirrorForge(ext *extension.Extension) uihandler.MirrorForgeFunc {
	return func(ctx context.Context, session, tenant, repo string) (*forge.Client, error) {
		if ext == nil {
			return nil, fmt.Errorf("the extension is not ready")
		}
		raw, _ := ext.GetConfig(tenant, "servers", session).(string)
		servers, err := forgeconfig.ParseServers(raw)
		if err != nil {
			return nil, fmt.Errorf("the `servers` config is malformed: %w", err)
		}
		cfg := forgeconfig.Config{Servers: servers}
		// THE PANEL PASSES `owner/name`, NOT A URL. HostOf parses a remote address, so it answers ""
		// for a bare path — and a "" host matches no server, so every panel request reported "not
		// configured" against a configuration that was in fact correct.
		//
		// The panel has no address to parse because it does not choose a host: the mirror is the one
		// this deployment is configured for. So the FIRST configured server is the one, and a bare
		// path is what selects it.
		host := forgeconfig.HostOf(repo)
		if host == "" {
			if len(cfg.Servers) == 0 {
				return nil, fmt.Errorf("no `servers` entry is configured for the mirror")
			}
			return forge.New(cfg.Servers[0].APIBase(), cfg.Servers[0].Token,
				"http://"+cfg.Servers[0].Host+"/"+strings.TrimPrefix(repo, "/")+".git")
		}
		srv, _ := cfg.ServerForHost(host)
		base := srv.APIBase()
		if base == "" {
			return nil, fmt.Errorf("%s is not configured as a forge host (set `kind` in the `servers` config)", host)
		}
		return forge.New(base, srv.Token, repo)
	}
}

func main() {

	log := slog.Default().With("svc", "gitdev-ext")

	natsURL := envOr("NATS_URL", "nats://127.0.0.1:4222")
	bus, err := nats.Connect(natsURL)
	if err != nil {
		log.Error("nats connect failed", "err", err)
		os.Exit(1)
	}

	m, err := manifest.ParseManifest(manifestYAML)
	if err != nil {
		log.Error("load manifest failed", "err", err)
		os.Exit(1)
	}

	// Workspaces are plain directories on this pod's disk. No database: a workspace is a git
	// checkout, and git already knows how to be one.
	var ext *extension.Extension
	/**
	 * THE SHARED JOB LIST, AND THE SWEEPER THAT FINISHES IT.
	 *
	 * A workflow run outlives the call that started it, and the answer arrives minutes later; the
	 * list is where `job-list`, `job-wait`, and any panel look for it. Without this a dispatched run
	 * is an id the caller has to poll for, which is what the tools used to tell it to do.
	 *
	 * THE SWEEPER IS THE SAME CODE EVERY EXTENSION RUNS. It watches the whole bucket and records a
	 * job as lost when its owner stops renewing — so whichever process is still alive is the one
	 * that notices a dead one.
	 */
	jobs := job.NewRegistry(bus)
	go job.NewSweeper(jobs).Run(context.Background())

	handlers := tools.Registry(tools.Deps{
		// Object storage for gh-artifact-download. An artifact archive is arbitrary in size, so
		// it is stored under a tenant-scoped key and referred to by name — never returned inline.
		PutObject: func(ctx context.Context, tenant, name string, data []byte) error {
			if ext == nil {
				return fmt.Errorf("extension not ready")
			}
			return ext.PutObject(ctx, tenant, name, data)
		},
		GetConfig: func(name, session, tenant string) any {
			if ext == nil {
				return nil
			}
			return ext.GetConfig(tenant, name, session)
		},
		Locale: func(session, tenant string) string {
			if ext == nil {
				return ""
			}
			return ext.GetSessionVariable(context.Background(), tenant, "agent", session, "locale", "")
		},
		// In-flight progress. The extension needs the callID to address an update, and only the
		// Execute adapter has it — so the adapter builds a reporter per call and puts it on the
		// context, and handlers reach it with tools.Progress(ctx).
		ReportProgress: func(ctx context.Context, tenant, callID string, p *easybus.ToolProgress) error {
			if ext == nil {
				return nil
			}
			return ext.ReportProgress(ctx, tenant, callID, p)
		},
		Jobs: jobs,
		/**
		 * THE WAKE, A CLOSURE OVER `ext` FOR THE SAME REASON AS ITS NEIGHBOURS: the extension is
		 * built after this struct, because it needs the tool specs this produces. Reading the
		 * variable when a run ends is always after start-up has finished; the nil check is for the
		 * window where that is not yet true.
		 */
		NotifyJobDone: func(ctx context.Context, tenant, session, jobID, title string, state job.State, exit *int) error {
			if ext == nil {
				return nil
			}
			return ext.PublishMailboxEvent(ctx, tenant, session, "trigger",
				tools.NewMailboxPayload(jobID, title, state, exit), "system:gitdev")
		},
	})

	specs := make(map[string]extension.ToolSpec, len(handlers))
	requires := tools.RequireMap()
	for name, h := range handlers {
		h := h
		specs[name] = extension.ToolSpec{
			// Attach the permission keys HERE rather than in each handler: the SDK needs them
			// before it calls the handler, and a handler that forgot to declare its own
			// requirement would simply never be gated.
			Requires: requires[name],
			Execute: func(ctx context.Context, args map[string]any, callID, session, tenant string) (extension.ToolResultData, error) {
				// A reporter per call: progress belongs to THIS call, and sharing one across
				// concurrent calls would interleave two tools' phases.
				// ext is nil while these specs are being built (it is assigned after
				// extension.New), so the reporter asks for it AT CALL TIME.
				rep := tools.NewReporter(func(p *easybus.ToolProgress) {
					// A failed progress publish must not fail the tool: the work is the point,
					// the narration is a courtesy.
					if e := ext; e != nil {
						if err := e.ReportProgress(ctx, tenant, callID, p); err != nil {
							log.Debug("progress publish failed", "tool", name, "err", err)
						}
					}
				})
				return h(tools.WithReporter(ctx, rep), args, session, tenant)
			},
		}
	}

	cfg := m.BuildConfig(manifest.Bindings{
		Handlers: specs,
		OnLifecycle: func(ctx context.Context, ev *easybus.LifecycleEvent, tenant string) error {
			// Logged rather than ignored: a deleted session is when its staging area should stop
			// being resumable, and that is worth seeing in the log.
			log.Info("lifecycle", "kind", easybus.LifecycleKindString(ev.Kind),
				"tenant", tenant, "session", ev.SessionName)
			return nil
		},
	})
	ext = extension.New(bus, cfg)

	if err := extension.Serve(ext, extension.ServeOptions{
		// The browser UI, served under /ext/gitdev and reverse-proxied there by the webui, so an
		// iframe embedding it is SAME-ORIGIN with the host. See extension.UISpec for why the prefix
		// matters.
		UI: &extension.UISpec{
			Assets: uiFiles,
			// THE BUNDLE IS ui/dist, not ui. fetch-ui.sh unrolls the release tarball into ui/dist and
			// leaves its own README and script beside it; a StripPrefix of "ui" would look for
			// ui/index.html, find nothing, and serve 404s for every asset with no clue why.
			StripPrefix: "ui/dist",
			Base:        uiBase,
			// TWO PREFIXES BELONG TO HANDLERS, NOT TO THE BUNDLE.
			//
			// `/api` is the probes. `/ui.v1` is the Connect service: its paths look like
			// `/ext/gitdev/ui.v1.UiService/GetWhoami`, which has no file extension, so the SPA
			// fallback would answer it with index.html — and the client would then fail parsing HTML
			// as protobuf, with no failing URL to point at.
			APIPrefix: "/api",
			Title:     "Git",
			// What the shelf says this is for. Declared HERE rather than in the manifest because it
			// describes the UI, and a build with no UI should declare none.
			Descriptions: map[string]string{
				"en": "Change code in a mirror without a clone, then land it on GitHub: pull requests, reviews and CI.",
				"zh": "无需克隆，在镜像里改代码，再经 GitHub 落地：合并请求、评审与 CI。",
			},
			// Served from this extension's own bundle, so the icon can change without a host release.
			Icon: "/icon.svg",
		},
		// API routes the UI reads from. They answer from the LOCAL workspace, never api.github.com:
		// see internal/uihandler.
		// AGENT_URL, not NATS: the UI API authenticates by asking the agent, which is the only
		// component that can map a bearer to a tenant.
		// The PR source resolves its token per request through the extension, because a token lives in
		// configuration and configuration can change while this pod is running. Holding one here
		// would pin whatever was set at boot.
		Handler: uiMux(
			m.Version,
			envOr("AGENT_URL", "http://agent.dev.svc.cluster.local"),
			&ghclient.PullSource{
				TokenFor: func(tenant string) string {
					if ext == nil {
						return ""
					}
					s, _ := ext.GetConfig(tenant, "token", "").(string)
					return s
				},
				// The handler authenticated already and put the tenant on the request context; this
				// reads it back rather than authenticating twice.
				TenantFrom: uihandler.TenantFromContext,
			},
			// Issues get their own page for the same reason pull requests have one: a number, a
			// title, a body and a conversation are things a reader reads in full, not in a card.
			&ghclient.IssueSource{
				TokenFor: func(tenant string) string {
					if ext == nil {
						return ""
					}
					s, _ := ext.GetConfig(tenant, "token", "").(string)
					return s
				},
				TenantFrom: uihandler.TenantFromContext,
			},
			// CI runs get pages for the same reason, and one more: a run's state CHANGES while it is
			// watched — steps turn green one at a time — where a card is a snapshot from the moment a
			// tool ran. Logs are fetched separately, one job at a time, because they are megabytes.
			&ghclient.RunSource{
				TokenFor: func(tenant string) string {
					if ext == nil {
						return ""
					}
					s, _ := ext.GetConfig(tenant, "token", "").(string)
					return s
				},
				TenantFrom: uihandler.TenantFromContext,
			},
			// The LAYER ABOVE a repository. Without it the panel opens on a page that says "open a
			// repository with #/files/…" and offers nothing to click.
			&ghclient.BrowseSource{
				TokenFor: func(tenant string) string {
					if ext == nil {
						return ""
					}
					s, _ := ext.GetConfig(tenant, "token", "").(string)
					return s
				},
				TenantFrom: uihandler.TenantFromContext,
			},
			// WHICH OF THOSE THE MIRROR HAS. The home page lists GitHub's repositories and the Files
			// page reads the mirror's, and until this nothing compared the two — a reader found out
			// by clicking a row that 404ed.
			&ghclient.MirroredSource{
				Cfg: func(tenant string) forgeconfig.Config {
					if ext == nil {
						return forgeconfig.Config{}
					}
					raw, _ := ext.GetConfig(tenant, "servers", "").(string)
					servers, err := forgeconfig.ParseServers(raw)
					if err != nil {
						return forgeconfig.Config{}
					}
					return forgeconfig.Config{Servers: servers}
				},
				TenantFrom: uihandler.TenantFromContext,
				// The GITHUB token, for fetching the upstream when a mirror is established. The
				// mirror's own token is in `servers`; this one reads GitHub.
				UpstreamToken: func(tenant string) string {
					if ext == nil {
						return ""
					}
					s, _ := ext.GetConfig(tenant, "token", "").(string)
					return s
				},
			},
			mirrorForge(ext),
		),
		Run: func(ctx context.Context, _ *extension.Extension) {
			log.Info("listening", "nats", natsURL)
			// Publish releases onto the `github` channel, so a session can follow a
			// repository and be woken when a version ships.
			startReleaseWatchers(ctx, ext, log)
		},
	}); err != nil {
		log.Error("serve failed", "err", err)
		os.Exit(1)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// depReporter returns the progress publisher, or nil when the extension is not ready.
//
// Read through a function so the adapter picks up the extension AFTER it is assigned: ext is nil
// while the specs are being built.
func depReporter(ext *extension.Extension) func(ctx context.Context, tenant, callID string, p *easybus.ToolProgress) error {
	if ext == nil {
		return nil
	}
	return ext.ReportProgress
}
