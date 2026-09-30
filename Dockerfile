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
COPY web/console/web/package.json web/console/web/package-lock.json ./
COPY web/console/web/vendor ./vendor
RUN npm ci --no-audit --no-fund

COPY web/console/web/ ./
RUN npm run build:swiss

# --- 2. the binary ---------------------------------------------------------
FROM golang:1.26 AS build

ARG GO_MOD_MODE=auto

WORKDIR /src
COPY go.mod go.sum ./
# RUN go mod download

COPY . .
# web/dist is committed empty so `go build` works without node; the real build
# lands here and is what gets embedded.
COPY --from=web /src/web/dist-swiss/ ./web/dist/

RUN set -eu ; \
    mod="${GO_MOD_MODE}"; \
    if [ "${mod}" = "auto" ]; then \
        if [ -f vendor/modules.txt ]; then mod=vendor; else mod=mod; fi; \
    fi; \
    echo "building with -mod=${mod}"; \
    if [ "${mod}" != "vendor" ]; then go mod download; fi; \
    CGO_ENABLED=0 GOOS=linux go build \
      -trimpath -mod="${mod}"\
      -ldflags="-s -w" \
      -o /out/swissd ./cmd/swissd

# --- 3. helm tooling -------------------------------------------------------
# swissd materialises a values file and a one-release helmfile into a temp dir
# and runs helmfile against it, so the same executor serves the CLI and the
# server. All three are static Go binaries and need no libc.
#
# Pinned: docs/deploy.md warns that helm here is v4 while much of the helm-diff
# ecosystem still assumes v3, and that mismatch now lives in this image.
FROM debian:bookworm-slim AS tools

ARG HELM_VERSION=v4.3.0
ARG HELMFILE_VERSION=1.8.0
ARG HELM_DIFF_VERSION=3.15.13
ARG TARGETARCH=amd64

RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates curl tar gzip \
 && rm -rf /var/lib/apt/lists/*

WORKDIR /tools
RUN curl -fsSL "https://get.helm.sh/helm-${HELM_VERSION}-linux-${TARGETARCH}.tar.gz" \
      | tar xz --strip-components=1 -C /tools "linux-${TARGETARCH}/helm" \
 && curl -fsSL "https://github.com/helmfile/helmfile/releases/download/v${HELMFILE_VERSION}/helmfile_${HELMFILE_VERSION}_linux_${TARGETARCH}.tar.gz" \
      | tar xz -C /tools helmfile \
 && mkdir -p /plugins/helm-diff \
 && curl -fsSL "https://github.com/databus23/helm-diff/releases/download/v${HELM_DIFF_VERSION}/helm-diff-linux-${TARGETARCH}.tgz" \
      | tar xz --strip-components=1 -C /plugins/helm-diff \
 && chmod +x /tools/helm /tools/helmfile

# --- 4. runtime ------------------------------------------------------------
# distroless/static: no shell, no package manager, no libc -- static Go binaries
# only, helm and helmfile included.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/swissd /swissd
COPY --from=tools /tools/helm /tools/helmfile /usr/local/bin/
COPY --from=tools /plugins /helm/plugins

# helm writes to all of these; the root filesystem is read-only in the chart, so
# they point at a mounted emptyDir.
ENV HELM_PLUGINS=/helm/plugins \
    HELM_CACHE_HOME=/tmp/helm/cache \
    HELM_CONFIG_HOME=/tmp/helm/config \
    HELM_DATA_HOME=/tmp/helm/data \
    TMPDIR=/tmp

USER nonroot:nonroot
EXPOSE 8080

# No shell means no shell-form ENTRYPOINT and no signal-swallowing wrapper:
# swissd receives SIGTERM directly and drains in-flight requests itself.
ENTRYPOINT ["/swissd"]
CMD ["--config", "/etc/swissd/swissd.yaml"]
