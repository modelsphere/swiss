package swisschart_test

import (
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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
	args := []string{"template", "swiss", chartDir(t), "--set", "rbac.namespaces={models}"}
	for _, s := range sets {
		args = append(args, "--set", s)
	}
	cmd := exec.Command("helm", args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestChartApplyWith(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}

	def, err := helmTemplate(t)
	if err != nil {
		t.Fatal(def)
	}
	if strings.Contains(def, "llmservices") || strings.Contains(def, "applyWith:") || strings.Contains(def, "controllerrevisions") {
		t.Fatalf("default render gained llmsvc output:\n%s", def)
	}

	helmDeploy, err := helmTemplate(t, "config.allowDeploy=true", "rbac.applyWith=helm")
	if err != nil {
		t.Fatal(helmDeploy)
	}
	if strings.Contains(helmDeploy, "llmservices") || strings.Contains(helmDeploy, "controllerrevisions") {
		t.Fatal("rbac.applyWith helm added LLMService rules")
	}
	if !strings.Contains(helmDeploy, "leaderworkersets") {
		t.Fatal("allowDeploy helm lost deployRules")
	}

	both, err := helmTemplate(t, "config.allowDeploy=true", "rbac.applyWith=both")
	if err != nil {
		t.Fatal(both)
	}
	if !strings.Contains(both, "leaderworkersets") || !strings.Contains(both, `resources: ["llmservices"]`) {
		t.Fatal("both should keep deployRules and add llmservices")
	}
	if !strings.Contains(both, `verbs: ["get","list","watch","create","update","patch","delete"]`) || !strings.Contains(both, "controllerrevisions") {
		t.Fatalf("both CRUD missing:\n%s", both)
	}

	only, err := helmTemplate(t, "config.allowDeploy=true", "config.applyWith=llmsvc", "rbac.applyWith=llmsvc")
	if err != nil {
		t.Fatal(only)
	}
	if strings.Contains(only, "leaderworkersets") || strings.Contains(only, "serviceaccounts") || strings.Contains(only, "poddisruptionbudgets") {
		t.Fatal("llmsvc mode still grants deployRules")
	}
	if !strings.Contains(only, "applyWith: llmsvc") || !strings.Contains(only, `resources: ["llmservices"]`) || !strings.Contains(only, "controllerrevisions") {
		t.Fatal("llmsvc mode missing its grant or server.applyWith")
	}
	if !strings.Contains(only, `resources: ["namespaces"]`) || !strings.Contains(only, `resources: ["modelroutes"]`) {
		t.Fatal("llmsvc mode missing namespaces or the read rules")
	}

	read, err := helmTemplate(t, "rbac.applyWith=both")
	if err != nil {
		t.Fatal(read)
	}
	if strings.Contains(read, "leaderworkersets") || strings.Contains(read, `verbs: ["get","list","watch","create","update","patch","delete"]`) {
		t.Fatal("without allowDeploy, both must stay read-only")
	}
	if !strings.Contains(read, `resources: ["llmservices"]`) || !strings.Contains(read, `verbs: ["get","list","watch"]`) || !strings.Contains(read, "controllerrevisions") {
		t.Fatalf("read view missing:\n%s", read)
	}

	cluster, err := helmTemplate(t, "rbac.scope=cluster", "config.allowDeploy=true", "rbac.applyWith=both")
	if err != nil {
		t.Fatal(cluster)
	}
	if !strings.Contains(cluster, "kind: ClusterRole") || !strings.Contains(cluster, "llmservices") || !strings.Contains(cluster, "leaderworkersets") {
		t.Fatal("cluster scope both should carry both grants on the ClusterRole")
	}

	for _, sets := range [][]string{
		{"config.applyWith=llmsvc"},
		{"config.applyWith=llmsvc", "rbac.applyWith=helm"},
		{"rbac.applyWith=llmsvc"},
		{"rbac.applyWith=llmsvc", "config.applyWith=helm"},
		{"config.applyWith=operator"},
		{"rbac.applyWith=operator"},
	} {
		out, err := helmTemplate(t, sets...)
		if err == nil {
			t.Fatalf("expected %v to fail:\n%s", sets, out)
		}
	}
}
