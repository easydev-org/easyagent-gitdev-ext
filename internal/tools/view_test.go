package tools

import "testing"

// A tool that names a workspace must emit a view pointing at it.
//
// This is the contract the host's panel depends on: without `data.view` the card shows no button,
// and the only symptom is a missing affordance — nothing errors. So each tool's view is pinned here
// next to the route shape the UI actually routes.
func TestViewRouteShapes(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"plain", wsRoute("ws-abc", "/tree/"), "/ws/ws-abc/tree/"},
		{"changes", wsRoute("ws-abc", "/changes"), "/ws/ws-abc/changes"},
		{"id is one segment", wsRoute("ws/a b", "/tree/"), "/ws/ws%2Fa%20b/tree/"},
		{"file path keeps slashes", wsRoute("w1", "/blob/"+escapePath("src/lib/a b.ts")),
			"/ws/w1/blob/src/lib/a%20b.ts"},
	}
	for _, c := range cases {
		if c.in != c.want {
			t.Errorf("%s: %q, want %q", c.name, c.in, c.want)
		}
	}
}

// A card names a RENDERER, not a route. There is nowhere to navigate to and nothing to fetch: the
// host hands the frame the tool result, and the UI draws it. That is why a template carries no
// workspace id — the card does not depend on one.
func TestCardForNamesATemplate(t *testing.T) {
	c := cardFor(cardTemplateFiles)
	if c == nil {
		t.Fatal("no card")
	}
	// The extension id, the Service prefix and the mount must agree; a mismatch shows up as a 404 on
	// every card rather than as wrong data, so it is worth pinning.
	if c["ext"] != "gitdev" {
		t.Errorf("ext = %v, want gitdev", c["ext"])
	}
	if c["template"] != cardTemplateFiles {
		t.Errorf("template = %v, want %v", c["template"], cardTemplateFiles)
	}
	// No route: a card that pointed at one would be a page again, and would inherit every way a page
	// can fail — the workspace expiring being the one that made this design wrong.
	if _, ok := c["route"]; ok {
		t.Errorf("a card must not carry a route: %#v", c)
	}
}

// An unnamed template is not a card, so a caller with nothing to say emits nothing.
func TestCardForNeedsATemplate(t *testing.T) {
	if c := cardFor(""); c != nil {
		t.Fatalf("an empty template produced a card: %#v", c)
	}
}
