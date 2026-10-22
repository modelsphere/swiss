package utils

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/onsi/ginkgo/v2"
)

const (
	defaultKindBinary  = "kind"
	defaultKindCluster = "swiss-test-e2e"
)

// Run executes cmd with its working directory set to the module root.
func Run(cmd *exec.Cmd) (string, error) {
	dir, err := GetProjectDir()
	if err != nil {
		return "", err
	}
	if cmd.Dir == "" {
		cmd.Dir = dir
	}
	if len(cmd.Env) == 0 {
		cmd.Env = append(os.Environ(), "GO111MODULE=on")
	}
	command := strings.Join(cmd.Args, " ")
	_, _ = fmt.Fprintf(ginkgo.GinkgoWriter, "running: %s\n", command)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s failed: %w\n%s", command, err, output)
	}
	return string(output), nil
}

// LoadImageToKindClusterWithName loads a local docker image into the kind cluster.
// KIND and KIND_CLUSTER override the binary and the cluster name.
func LoadImageToKindClusterWithName(name string) error {
	cluster := defaultKindCluster
	if v, ok := os.LookupEnv("KIND_CLUSTER"); ok && v != "" {
		cluster = v
	}
	kindBinary := defaultKindBinary
	if v, ok := os.LookupEnv("KIND"); ok && v != "" {
		kindBinary = v
	}
	cmd := exec.Command(kindBinary, "load", "docker-image", name, "--name", cluster)
	_, err := Run(cmd)
	return err
}

// GetNonEmptyLines splits output on newlines and drops empty lines.
func GetNonEmptyLines(output string) []string {
	var res []string
	for line := range strings.SplitSeq(output, "\n") {
		if line != "" {
			res = append(res, line)
		}
	}
	return res
}

// GetProjectDir returns the swiss module root above the working directory.
// web/console is its own module, so the walk checks the module path.
func GetProjectDir() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get working directory: %w", err)
	}
	dir := wd
	for {
		if isSwissModule(filepath.Join(dir, "go.mod")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("swiss module not found above %s", wd)
		}
		dir = parent
	}
}

func isSwissModule(path string) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			return strings.TrimSpace(line) == "module github.com/modelsphere/swiss"
		}
	}
	return false
}
