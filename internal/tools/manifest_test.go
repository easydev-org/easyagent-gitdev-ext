package tools

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/easydev-org/easybus-sdk/manifest"

	"github.com/easydev-org/easyagent-gitdev-ext/internal/watch"
)

// The manifest is embedded from cmd/gitdev-ext/manifest.yaml, and the repository root carries a
// copy for readers. Two copies drift — so this asserts they are identical, and that the
// manifest agrees with the Go code about which tools exist and which need permissions.

func loadManifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "manifest.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	m, err := manifest.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse manifest: %v", err)
	}
	return m
}

// The embedded copy is what actually ships; if it differs from the root copy, one of them is
// wrong and the reader is looking at the wrong one.
func TestEmbeddedManifestMatchesRoot(t *testing.T) {
	root := filepath.Join("..", "..")
	a, err := os.ReadFile(filepath.Join(root, "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "cmd", "gitdev-ext", "manifest.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("manifest.yaml and cmd/gitdev-ext/manifest.yaml have drifted; copy one over the other")
	}
}

// Every handler must be declared, and every declared tool must have a handler. A tool in the
// manifest with no handler is advertised and then fails at call time; a handler with no
// declaration is unreachable.
func TestRegistryMatchesManifest(t *testing.T) {
	declared := map[string]bool{}
	for _, tl := range loadManifest(t).Tools {
		declared[tl.Name] = true
	}
	handlers := Registry(Deps{})

	for name := range handlers {
		if !declared[name] {
			t.Errorf("handler %q is not declared in manifest.yaml", name)
		}
	}
	for name := range declared {
		if _, ok := handlers[name]; !ok {
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
}

// The permission each tool declares must match what the Go code enforces.
//
// Checked because the two are consumed by different layers — the agent reads the manifest, the
// extension checks Require — and a mismatch means either a tool that looks gated and is not, or
// a manifest that hides a requirement.
func TestRequiresMatchManifest(t *testing.T) {
	m := loadManifest(t)
	goRequires := RequireMap()

	seen := map[string]bool{}
	for _, tl := range m.Tools {
		want := goRequires[tl.Name]
		seen[tl.Name] = true
		if len(want) != len(tl.Requires) {
			t.Errorf("%s: manifest requires %v, code enforces %v", tl.Name, tl.Requires, want)
			continue
		}
		for i := range want {
			if want[i] != tl.Requires[i] {
				t.Errorf("%s: manifest requires %v, code enforces %v", tl.Name, tl.Requires, want)
				break
			}
		}
	}
	for name := range goRequires {
		if !seen[name] {
			t.Errorf("code requires a permission for %q, which the manifest does not declare", name)
		}
	}
}

// Every permission a tool requires must be DECLARED, or the agent has no key to check.
func TestRequiredPermissionsAreDeclared(t *testing.T) {
	m := loadManifest(t)
	declared := map[string]bool{}
	for _, p := range m.Permissions {
		declared[p.Key] = true
	}
	for tool, keys := range RequireMap() {
		for _, k := range keys {
			if !declared[k] {
				t.Errorf("%s requires %q, which the manifest does not declare", tool, k)
			}
		}
	}
}

// THE DECLARED CHANNEL MUST BE THE ONE THE CODE PUBLISHES ON.
//
// A subscription stores the channel as a literal string and the router compares it with `==`, so a
// declaration that disagrees with the publisher produces a subscription that matches NOTHING — no
// error at subscribe time, no error at publish time, and the session simply never hears the event.
//
// It happened: the manifest said `github` while release.go published on `gitdev`.
func TestDeclaredChannelMatchesPublishedChannel(t *testing.T) {
	m := loadManifest(t)
	if len(m.Channels) == 0 {
		t.Fatal("manifest declares no channels")
	}
	for _, c := range m.Channels {
		if c.Name == watch.ChannelName {
			return
		}
	}
	var got []string
	for _, c := range m.Channels {
		got = append(got, c.Name)
	}
	t.Fatalf("release watching publishes on %q, but the manifest declares %v; "+
		"a subscriber following the declared name would never receive an event",
		watch.ChannelName, got)
}
