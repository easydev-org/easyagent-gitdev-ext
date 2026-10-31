#!/bin/sh
# Fetch the built panel from its own repository into ./ui, where go:embed picks it up.
#
# WHY THE UI IS STILL EMBEDDED even though it is a separate project: the binary must be
# self-contained. A panel that shipped as a second image would be a deployment that can come up with
# the UI and the API at different versions — which is the whole class of bug this split exists to
# remove, not to reintroduce one layer up.
#
# THE PANEL'S SOURCE LIVES ELSEWHERE. What lands here is `dist/`, built from
# easydev-org/easyagent-gitdev-ext-ui by that repository's own CI, and pinned by UI_VERSION so a build
# of this extension is reproducible.
#
# Run by `go generate ./...` and by the Containerfile. It is deliberately IDEMPOTENT and OFFLINE-CLEAN
# ON REPEAT: an unchanged version is not fetched twice, so a rebuild costs nothing.
set -eu

UI_VERSION="${UI_VERSION:-v0.1.0}"
REPO="${UI_REPO:-easydev-org/easyagent-gitdev-ext-ui}"
HERE="$(cd "$(dirname "$0")" && pwd)"
STAMP="$HERE/.version"

if [ -f "$STAMP" ] && [ "$(cat "$STAMP")" = "$UI_VERSION" ] && [ -f "$HERE/dist/index.html" ]; then
  echo "ui: already at $UI_VERSION"
  exit 0
fi

echo "ui: fetching $UI_VERSION from $REPO"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

# THE RELEASE ASSET, NOT THE SOURCE ARCHIVE.
#
# The panel's repository does not commit its build output — a generated directory in every diff, with
# no version to pin. What it publishes is `ui.tar.gz` attached to each release, and that is a thing
# with a tag and a digest. Fetching the source archive instead would download a tree whose `dist/`
# does not exist, which is a confusing failure three steps later ("the archive has no dist/").
url="https://github.com/${REPO}/releases/download/${UI_VERSION}/ui.tar.gz"

# THREE WAYS TO FETCH, because this runs in three places with different tools: a CI runner with curl, a
# slim container with neither, and a developer's machine where python3 lives somewhere odd. Failing
# here with "wget: not found" is a build error that explains nothing about what was being downloaded.
#
# `command -v` is not enough on its own: python3 installed by a version manager is often absent from a
# non-interactive shell's PATH. So the fallbacks are tried in order and each one is checked by whether
# it produced a file.
try_python() {
  for py in python3 python /usr/bin/python3 /usr/local/bin/python3; do
    if command -v "$py" >/dev/null 2>&1; then
      "$py" -c 'import sys,urllib.request;urllib.request.urlretrieve(sys.argv[1],sys.argv[2])' "$1" "$2" && return 0
    fi
  done
  return 1
}

fetched=0
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$url" -o "$work/ui.tgz" && fetched=1
elif command -v wget >/dev/null 2>&1; then
  wget -q "$url" -O "$work/ui.tgz" && fetched=1
fi
if [ "$fetched" != 1 ]; then
  try_python "$url" "$work/ui.tgz" && fetched=1
fi
if [ "$fetched" != 1 ] || [ ! -s "$work/ui.tgz" ]; then
  echo "ui: cannot download $url — no curl, wget or a usable python3 on this machine" >&2
  exit 1
fi

# THE TARBALL IS THE BUNDLE ITSELF — its members are dist/'s CONTENTS, so it unrolls straight into
# place. (The first version expected a source tree with a dist/ inside, which is what a GitHub source
# archive would give and what a release asset is not.)
mkdir -p "$work/unpacked"
tar -xzf "$work/ui.tgz" -C "$work/unpacked"
if [ ! -f "$work/unpacked/index.html" ]; then
  echo "ui: the bundle has no index.html — is $UI_VERSION a panel release with ui.tar.gz attached?" >&2
  find "$work/unpacked" -maxdepth 2 | head -20 >&2
  exit 1
fi

rm -rf "$HERE/dist" "$HERE/index.html"
mv "$work/unpacked" "$HERE/dist"
echo "$UI_VERSION" > "$STAMP"
echo "ui: $UI_VERSION in place ($(find "$HERE/dist" -type f | wc -l) files)"
