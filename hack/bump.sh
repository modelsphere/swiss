#!/usr/bin/env bash
# Bump the one version Swiss has.
#
#   ./hack/bump.sh                 show the current version
#   ./hack/bump.sh patch|minor|major
#   ./hack/bump.sh 0.3.1           set it explicitly
#   ./hack/bump.sh minor --tag     and create an annotated git tag
#
# Two files hold it -- Chart.yaml and internal/version -- and this moves both.
# The version is a Go constant rather than an ldflags stamp so that `go build`
# from anywhere reports the truth and a forgotten build argument cannot ship an
# image calling itself 0.0.0-dev. A test in internal/version refuses a commit
# where the two disagree, which is what makes two files safe.
#
# image.tag stays empty in the chart, so helm follows appVersion.
set -euo pipefail
cd "$(dirname "$0")/.."

chart=helm/swiss/Chart.yaml
gofile=internal/version/version.go
current="$(yq -r '.appVersion' "$chart")"

if [ $# -eq 0 ]; then
  echo "$current"
  exit 0
fi

next=""
case "$1" in
  major|minor|patch)
    IFS=. read -r a b c <<< "$current"
    case "$1" in
      major) next="$((a + 1)).0.0" ;;
      minor) next="${a}.$((b + 1)).0" ;;
      patch) next="${a}.${b}.$((c + 1))" ;;
    esac
    ;;
  [0-9]*) next="$1" ;;
  *) echo "usage: $0 [major|minor|patch|X.Y.Z] [--tag]" >&2; exit 2 ;;
esac

if ! [[ "$next" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
  echo "not a semver: $next" >&2
  exit 2
fi

tag=false
[ "${2:-}" = "--tag" ] && tag=true
if $tag && [ -n "$(git status --porcelain)" ]; then
  echo "working tree is dirty; commit before tagging" >&2
  exit 1
fi

# Chart version and appVersion move together: this chart exists only to deploy
# this app, so a packaging change without an app change is not a thing that
# happens here.
#
# Line-targeted rather than `yq -i`: yq rewrites the whole document, which drops
# Chart.yaml's trailing comment and reflows the description. A version bump must
# not silently delete documentation.
tmp="$(mktemp)"
sed -e "s/^version: .*/version: $next/" \
    -e "s/^appVersion: .*/appVersion: \"$next\"/" \
    "$chart" > "$tmp"
mv "$tmp" "$chart"

sed -e "s/^const Version = .*/const Version = \"$next\"/" "$gofile" > "$tmp"
mv "$tmp" "$gofile"

# internal/version has a test asserting these two agree; check it here too, so a
# bump that half-applied fails now rather than in CI.
if [ "$(yq -r '.appVersion' "$chart")" != "$next" ] || ! grep -q "\"$next\"" "$gofile"; then
  echo "bump did not take; check $chart and $gofile" >&2
  exit 1
fi

# A bump that leaves the chart unrenderable is worth catching now, not at deploy.
helm lint helm/swiss --set config.cluster.name=x --set 'rbac.namespaces={y}' >/dev/null

echo "$current -> $next"

if $tag; then
  git add "$chart" "$gofile"
  git commit -m "swiss: $next"
  git tag -a "swiss-v$next" -m "swiss $next"
  echo "tagged swiss-v$next"
fi

cat <<NEXT

build and deploy:
  docker build -t harbor.4pd.io/hardcore-tech/swissd:$next .
  docker push harbor.4pd.io/hardcore-tech/swissd:$next
  helm upgrade swiss ./helm/swiss -n swiss-system
NEXT
