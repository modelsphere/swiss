// Package version carries the one version constant.
//
// In source rather than stamped by ldflags: `go build ./cmd/swissd` from
// anywhere then reports the truth, and a forgotten --build-arg cannot ship an
// image that calls itself 0.0.0-dev. hack/bump.sh moves this and Chart.yaml
// together, and a test here refuses a commit where they disagree.
package version

const Version = "0.2.1"
