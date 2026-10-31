# syntax=docker/dockerfile:1
# A static Go binary. go-git is pure Go, so no system git is needed, and the runtime image is a
# bare alpine base.
#
# PORTABILITY: nothing environment-specific lives here. No internal mirror, no proxy, no
# *.svc.cluster.local. A CI runner and a laptop build the same image; anything a particular
# environment needs is supplied as a build-arg or by the container runtime.

# THE PANEL IS BUILT ELSEWHERE NOW, and fetched here.
#
# It used to be a node stage: copy ui/, npm ci, npm run build. ui/ is no longer source — the panel is
# easydev-org/easyagent-gitdev-ext-ui, built by its own CI, published as ui.tar.gz on a release. So
# this stage downloads that tarball and unrolls it, which also means the Go build no longer needs Node.
#
# THE VERSION IS PINNED BY UI_VERSION. A moving target would make an image unreproducible, and the
# whole point of a release asset is that it has a tag to pin.
#
# The script is committed at cmd/gitdev-ext/ui/fetch-ui.sh, next to the //go:embed that consumes its
# output, because go:embed cannot reach outside the package directory.
FROM alpine:3.24 AS ui
RUN apk add --no-cache ca-certificates tar gzip curl
WORKDIR /ui
ARG UI_VERSION=v0.1.4
COPY cmd/gitdev-ext/ui/fetch-ui.sh ./fetch-ui.sh
RUN UI_VERSION="${UI_VERSION}" sh ./fetch-ui.sh
# FAIL THE BUILD, NOT THE DEPLOY, IF THE BUNDLE IS EMPTY.
#
# go:embed of an empty directory is legal, so a UI stage that produced nothing yields a binary with no
# assets and an extension that 404s every UI path — with a green CI. That is exactly what happened
# once already, when a job that built the bundle separately passed while the image shipped without it.
RUN test -f /ui/dist/index.html || (echo "FATAL: the bundle has no index.html" >&2; ls -la /ui; exit 1)
# And that it is the bundle we asked for, not a cached layer from another version.
RUN test "$(cat /ui/.version)" = "${UI_VERSION}" || (echo "FATAL: fetched $(cat /ui/.version), wanted ${UI_VERSION}" >&2; exit 1)

FROM golang:1.26-alpine AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# go:embed cannot reach outside the package directory, so the fetched bundle is placed where
# cmd/gitdev-ext/ui declares it. See the //go:embed directive in main.go.
#
# ONLY dist/ AND .version ARE COPIED. The stage's work directory also holds the script and a tarball;
# embedding those would put a shell script inside the served asset tree.
RUN rm -rf ./cmd/gitdev-ext/ui/dist
COPY --from=ui /ui/dist ./cmd/gitdev-ext/ui/dist
COPY --from=ui /ui/.version ./cmd/gitdev-ext/ui/.version
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/gitdev-ext ./cmd/gitdev-ext

FROM alpine:3.24
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=build /out/gitdev-ext /app/gitdev-ext

# NO WORKSPACE AND NO VOLUME. There is nothing on this container's disk worth keeping: a change is
# made through the forge's API, so a branch is a ref on the server rather than a directory here, and
# a restart loses only what could be recomputed anyway.
ENTRYPOINT ["/app/gitdev-ext"]
