# swissd: the server, with the SPA embedded in it.
#
# Three stages, because the UI is compiled into the Go binary -- the web build
# has to finish before the Go build starts, and neither toolchain belongs in the
# runtime image.

# Debian-based build stages, not Alpine. Alpine links against musl, which
# differs from glibc in DNS resolution, thread stack sizes and malloc behaviour;
# node's native addons and the Go toolchain both behave subtly differently there,
# and the runtime below is Debian-derived anyway, so matching removes a class of
# "works on my builder" difference for no size benefit worth having.

# --- 1. the SPA ------------------------------------------------------------
FROM node:24-bookworm-slim AS web

WORKDIR /src/web
# Manifests first: this layer is cached until a dependency actually changes,
# which is most of the build time.
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund

COPY web/ ./
RUN npm run build

# --- 2. the binary ---------------------------------------------------------
FROM golang:1.27-bookworm AS build

ARG VERSION=0.0.0-dev

WORKDIR /src
COPY go.mod go.sum ./
# RUN go mod download

COPY . .
# web/dist is committed empty so `go build` works without node; the real build
# lands here and is what gets embedded.
COPY --from=web /src/web/dist ./web/dist

# CGO off for a static binary the distroless runtime can run. -trimpath keeps
# build paths out of the binary, so two builds of one commit are comparable.
RUN set -eu ; \
    mod="${GO_MOD_MODE}"; \
    if [ "${mod}" = "auto" ]; then \
        if [ -f vendor/modules.txt ]; then mod=vendor; else mod=mod; fi; \
    fi; \
    echo "building with -mod=${mod}"; \
    if [ "${mod}" != "vendor" ]; then go mod download; fi; \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -mod="${mod}"\
      -ldflags="-s -w -X main.version=${VERSION}" \
      -o /out/swissd ./cmd/swissd

# --- 3. runtime ------------------------------------------------------------
# distroless/static: no shell, no package manager, no libc. swissd reads the
# cluster through its ServiceAccount and serves HTTP; it needs nothing else, and
# what is not in the image cannot be used against it.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/swissd /swissd

USER nonroot:nonroot
EXPOSE 8080

# No shell means no shell-form ENTRYPOINT and no signal-swallowing wrapper:
# swissd receives SIGTERM directly and drains in-flight requests itself.
ENTRYPOINT ["/swissd"]
CMD ["--config", "/etc/swissd/swissd.yaml"]
