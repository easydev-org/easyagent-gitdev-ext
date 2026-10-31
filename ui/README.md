# ui/

**Nothing here is source.** This directory holds the BUILT panel, fetched from
[easyagent-gitdev-ext-ui](https://github.com/easydev-org/easyagent-gitdev-ext-ui) by
`fetch-ui.sh`, and embedded into the binary by `//go:embed all:ui`.

## Why a built artifact and not the source

The panel is a separate project with its own toolchain, its own release cadence, and its own CI. Its
source does not belong in a Go module — and a Go build should not need Node.

## Why embedded at all

The binary must be self-contained. A panel that shipped as a second image would be a deployment that
can come up with the UI and the API at different versions, which is the class of bug this split exists
to remove.

## Refreshing it

```sh
UI_VERSION=v0.2.0 ./ui/fetch-ui.sh   # a tag, for a release
UI_VERSION=main   ./ui/fetch-ui.sh   # the tip, for working on both together
```

`dist/` and `.version` are committed so a checkout builds without network access. `go generate ./...`
runs the script, and the Containerfile runs it too.
