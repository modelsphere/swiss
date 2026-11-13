package swisschart_test

import (
	"os/exec"
	"strings"
	"testing"
)

// minimax-h3 renders a JobSLORequirement per pool and a PVC for its asset
// volume; swissd runs helm with its own ServiceAccount, so both are in the
// deploy grant at either scope.
func TestChartGrantsWhatAVideoChartRenders(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	for _, sets := range [][]string{nil, {"rbac.scope=cluster"}} {
		out, err := helmTemplate(t, sets...)
		if err != nil {
			t.Fatal(out)
		}
		for _, want := range []string{
			`resources: ["llmslorequirements", "jobslorequirements"]`,
			`"persistentvolumeclaims"`,
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%v: the deploy grant is missing %s", sets, want)
			}
		}
	}
}

// A Secret a chart reads with lookup is granted get by name, in its own
// namespace, and nothing more.
func TestChartGrantsLookupSecretsByName(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t, "rbac.lookupSecrets[0].namespace=middleware", "rbac.lookupSecrets[0].name=cnpg-rw")
	if err != nil {
		t.Fatal(out)
	}
	var role string
	for _, doc := range strings.Split(out, "\n---") {
		if strings.Contains(doc, "kind: Role\nmetadata:\n  name: swiss-lookup") {
			role = doc
		}
	}
	for _, want := range []string{"namespace: middleware", `resourceNames: ["cnpg-rw"]`, `verbs: ["get"]`, `apiGroups: ["postgresql.cnpg.io"]`, `resources: ["databases"]`} {
		if !strings.Contains(role, want) {
			t.Errorf("lookup Role is missing %s:\n%s", want, role)
		}
	}
	if !strings.Contains(out, "kind: RoleBinding") || !strings.Contains(out, "name: swiss-lookup") {
		t.Error("the lookup Role is not bound")
	}
	if none, err := helmTemplate(t); err != nil || strings.Contains(none, "swiss-lookup") {
		t.Errorf("no lookupSecrets must render no lookup Role (err %v)", err)
	}
	if bad, err := helmTemplate(t, "rbac.lookupSecrets[0].namespace=middleware"); err == nil || !strings.Contains(bad, "need namespace and name") {
		t.Errorf("an entry without a name must fail the render, got %v", err)
	}
}

// minimax-h3 renders each release's CloudNativePG Database in the cluster's
// namespace: cluster scope grants Databases everywhere, namespaced scope only
// beside the Secrets it looks up.
func TestChartGrantsCNPGDatabases(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	cluster, err := helmTemplate(t, "rbac.scope=cluster")
	if err != nil {
		t.Fatal(cluster)
	}
	if !strings.Contains(cluster, `apiGroups: ["postgresql.cnpg.io"]`) {
		t.Error("cluster scope does not grant CloudNativePG Databases")
	}
	none, err := helmTemplate(t)
	if err != nil {
		t.Fatal(none)
	}
	if strings.Contains(none, "postgresql.cnpg.io") {
		t.Error("namespaced scope without lookupSecrets must not grant Databases anywhere")
	}
}
