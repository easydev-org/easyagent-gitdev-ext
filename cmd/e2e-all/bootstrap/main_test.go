package main

import "testing"

// The name the bootstrap reports has to be OWNER/NAME, because every git-* tool builds a remote URL
// from it and the extension derives the owner by parsing that URL.
//
// THIS EXISTS BECAUSE IT WAS WRONG AND COMPILED. The value was taken from Establish's first return —
// the branch — so the mirror's identity came out as `main` and the suite failed with
// `cannot derive owner/repo from "http://127.0.0.1:3000/main.git"`. A string with the right shape
// checked here would have caught it in a second.
func TestMirrorNameIsOwnerSlashName(t *testing.T) {
	for _, tc := range []struct{ raw, wantOwner, wantName string }{
		{"https://github.com/easydev-org/test.git", "easydev-org", "test"},
		{"https://github.com/easydev-org/test", "easydev-org", "test"},
		{"git@github.com:easydev-org/test.git", "easydev-org", "test"},
		{"https://github.com/abcp-sdk/abc-protocol-go.git", "abcp-sdk", "abc-protocol-go"},
	} {
		owner, name := ownerAndName(tc.raw)
		if owner != tc.wantOwner || name != tc.wantName {
			t.Errorf("ownerAndName(%q) = %q, %q; want %q, %q",
				tc.raw, owner, name, tc.wantOwner, tc.wantName)
		}
	}
}

// A URL with no owner is reported as having none, rather than producing something that looks usable.
func TestOwnerAndNameRejectsBareNames(t *testing.T) {
	for _, raw := range []string{"test", "https://github.com/test", ""} {
		if owner, _ := ownerAndName(raw); owner != "" {
			t.Errorf("ownerAndName(%q) gave owner %q; want none", raw, owner)
		}
	}
}
