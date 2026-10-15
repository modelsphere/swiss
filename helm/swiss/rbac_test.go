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
	if !strings.Contains(helmDeploy, "leaderworkersets") || !strings.Contains(helmDeploy, "disaggregatedsets") {
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

	only, err := helmTemplate(t, "config.allowDeploy=true", "rbac.applyWith=llmsvc")
	if err != nil {
		t.Fatal(only)
	}
	if strings.Contains(only, "leaderworkersets") || strings.Contains(only, "serviceaccounts") || strings.Contains(only, "poddisruptionbudgets") {
		t.Fatal("llmsvc mode still grants deployRules")
	}
	if strings.Contains(only, "applyWith:") {
		t.Fatal("the chart must not render applyWith; that is the site profile")
	}
	if !strings.Contains(only, `resources: ["llmservices"]`) || !strings.Contains(only, "controllerrevisions") {
		t.Fatal("llmsvc mode missing its grant")
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

	// The grant and the backend are no longer paired. llmsvc without
	// allowDeploy is the read view, and an unknown grant is still refused.
	readLLM, err := helmTemplate(t, "rbac.applyWith=llmsvc")
	if err != nil {
		t.Fatal(readLLM)
	}
	if strings.Contains(readLLM, "leaderworkersets") || strings.Contains(readLLM, "applyWith:") {
		t.Fatal("llmsvc without allowDeploy must stay a read grant and must not render applyWith")
	}
	if !strings.Contains(readLLM, `resources: ["llmservices"]`) || !strings.Contains(readLLM, `verbs: ["get","list","watch"]`) {
		t.Fatalf("llmsvc read view missing:\n%s", readLLM)
	}

	out, err := helmTemplate(t, "rbac.applyWith=operator")
	if err == nil {
		t.Fatalf("expected rbac.applyWith=operator to fail:\n%s", out)
	}
}
