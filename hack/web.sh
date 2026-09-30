#!/usr/bin/env bash
# Build the UI into web/dist, which `go build` embeds -- what the Docker web stage
# does. The source is console's web/ at the web/console submodule's commit; its
# swiss variant is swiss's own shell and login.
#
#   hack/web.sh
#   git -C web/console checkout <commit> && git add web/console   # move the pin
set -euo pipefail
cd "$(dirname "$0")/.."

[ -f web/console/web/package.json ] || git submodule update --init web/console
(cd web/console/web && npm ci --no-audit --no-fund && npm run build:swiss)
find web/dist -mindepth 1 ! -name .gitkeep -delete
cp -R web/console/web/dist-swiss/. web/dist/
echo "web/dist <- console $(git -C web/console rev-parse --short HEAD)"
