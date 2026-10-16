package swisschart_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func chartDir(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller")
	}
	return filepath.Dir(file)
}

func helmTemplate(t *testing.T, sets ...string) (string, error) {
	t.Helper()
	return helmTemplateIn(t, "default", append([]string{"rbac.namespaces={models}"}, sets...)...)
}

func helmTemplateIn(t *testing.T, namespace string, sets ...string) (string, error) {
	t.Helper()
	args := []string{"template", "swiss", chartDir(t), "--namespace", namespace}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// The grant does not depend on how a release is installed: the method is picked
// per install, so every release Role carries both the helm rules and LLMService.
func TestChartGrantsLLMService(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	crud := `verbs: ["get","list","watch","create","update","patch","delete"]`

	read, err := helmTemplate(t, "config.allowDeploy=false")
	if err != nil {
		t.Fatal(read)
	}
	if strings.Contains(read, "leaderworkersets") || strings.Contains(read, crud) {
		t.Fatal("without allowDeploy the release Role must stay read-only")
	}
	if !strings.Contains(read, `resources: ["llmservices"]`) || !strings.Contains(read, `verbs: ["get","list","watch"]`) || !strings.Contains(read, "controllerrevisions") {
		t.Fatalf("read view missing llmservices:\n%s", read)
	}
	if !strings.Contains(read, `resources: ["modelroutes"]`) {
		t.Fatal("read view lost the object read rules")
	}

	// Deploy is the chart's default.
	deploy, err := helmTemplate(t)
	if err != nil {
		t.Fatal(deploy)
	}
	if !strings.Contains(deploy, "leaderworkersets") || !strings.Contains(deploy, "disaggregatedsets") {
		t.Fatal("allowDeploy lost deployRules")
	}
	if !strings.Contains(deploy, `resources: ["llmservices"]`) || !strings.Contains(deploy, crud) || !strings.Contains(deploy, "controllerrevisions") {
		t.Fatalf("allowDeploy missing llmservices CRUD:\n%s", deploy)
	}
	if !strings.Contains(deploy, `resources: ["namespaces"]`) {
		t.Fatal("allowDeploy missing the namespaces ClusterRole")
	}
	if strings.Contains(deploy, "applyWith:") {
		t.Fatal("the chart must not render applyWith; that is the site profile")
	}

	cluster, err := helmTemplate(t, "rbac.scope=cluster")
	if err != nil {
		t.Fatal(cluster)
	}
	if !strings.Contains(cluster, "kind: ClusterRole") || !strings.Contains(cluster, "llmservices") || !strings.Contains(cluster, "leaderworkersets") {
		t.Fatal("cluster scope should carry both grants on the ClusterRole")
	}
}

// Two installs under one release name, each in its own namespace and managing
// only that namespace, must not render an object the other also owns.
func TestChartInstallsSideBySide(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	owned := map[string]string{}
	for _, ns := range []string{"team-a", "team-b"} {
		out, err := helmTemplateIn(t, ns, "rbac.namespaces={"+ns+"}")
		if err != nil {
			t.Fatal(out)
		}
		for _, id := range objectIDs(t, out, ns) {
			if other, ok := owned[id]; ok {
				t.Errorf("%s is rendered by both the %s and the %s install", id, other, ns)
			}
			owned[id] = ns
		}
	}
}

// objectIDs is kind/namespace/name per rendered object; cluster-scoped kinds
// have no namespace, and a namespaced one without its own is the release's.
func objectIDs(t *testing.T, rendered, releaseNS string) []string {
	t.Helper()
	var ids []string
	for _, doc := range strings.Split(rendered, "\n---") {
		var obj struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		if obj.Kind == "" {
			continue
		}
		ns := obj.Metadata.Namespace
		switch {
		case strings.HasPrefix(obj.Kind, "Cluster"):
			ns = ""
		case ns == "":
			ns = releaseNS
		}
		ids = append(ids, obj.Kind+"/"+ns+"/"+obj.Metadata.Name)
	}
	return ids
}
