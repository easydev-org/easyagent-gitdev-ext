# syntax=docker/dockerfile:1
# A static Go binary. go-git is pure Go, so no system git is needed, and the runtime image is a
# bare alpine base.
#
# PORTABILITY: nothing environment-specific lives here. No internal mirror, no proxy, no
# *.svc.cluster.local. A CI runner and a laptop build the same image; anything a particular
# environment needs is supplied as a build-arg or by the container runtime.

# The browser UI, built FIRST and copied into the Go tree before `go build`.
#
# It is a build stage rather than committed output: a stale dist/ in the repository would be served
# in place of the real one, and the failure looks like "the UI is wrong" rather than "the build is
# wrong". Keeping the toolchain here also means a contributor needs nothing but Docker.
FROM node:26-alpine AS ui
WORKDIR /ui
# Bump this to force `npm run build` to re-run.
#
# Docker caches a layer on its inputs; when a source file changes the COPY invalidates it, but when
# only the BUILD CACHE is stale (a restored gha cache from an earlier run) the old dist/ can be
# reused. It is rare and it is invisible: the image builds, the UI loads, and it behaves like the
# previous version.
ARG UI_BUILD=2
RUN echo "ui build ${UI_BUILD}"
# The lockfile is committed (see .gitignore: node_modules is not, the lock is). `npm ci` requires it,
# so the image is reproducible and a drift between package.json and the lock fails here instead of
# resolving something new at build time.
COPY ui/package.json ui/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY ui/ ./
RUN npm run build
# Fail the BUILD, not the deploy, if the bundle is empty.
#
# go:embed of an empty directory is legal, so a UI stage that produced nothing yields a binary with
# no assets and an extension that 404s every UI path — with a green CI. That is exactly what
# happened: the `ui` job passed (it builds the bundle separately) while the image shipped without it.
# Checking here makes the failure land where the mistake is.
RUN test -f /ui/dist/index.html || (echo "FATAL: vite produced no index.html" >&2; ls -la /ui; exit 1)
# And that the bundle is not a stale layer: this marker only exists in the current API client.
#
# A cached `npm run build` ships a bundle that looks fine and behaves like the previous version,
# which is indistinguishable from "the fix did not work" when all you have is a browser. Greping a
# marker turns that into a build failure that names the marker.
RUN grep -q 'agent.token' /ui/dist/assets/*.js || (echo "FATAL: the built bundle is STALE (no agent.token in dist); the build layer was cached" >&2; ls -la /ui/dist/assets; exit 1)

FROM golang:1.26-alpine AS build
WORKDIR /build
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
# go:embed cannot reach outside the package directory, so the built UI is placed where
# cmd/gitdev-ext/ui declares it. See the //go:embed directive in main.go.
COPY --from=ui /ui/dist ./cmd/gitdev-ext/ui
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
