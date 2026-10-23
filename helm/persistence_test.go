package swisschart_test

import (
	"os/exec"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// dataVol is the swissd Deployment's data volume: a claim, or an emptyDir.
func dataVol(t *testing.T, rendered string) (claim string, emptyDir bool, sizeLimit string) {
	t.Helper()
	for _, doc := range strings.Split(rendered, "\n---") {
		var obj struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Template struct {
					Spec struct {
						Volumes []struct {
							Name                  string `yaml:"name"`
							PersistentVolumeClaim *struct {
								ClaimName string `yaml:"claimName"`
							} `yaml:"persistentVolumeClaim"`
							EmptyDir *struct {
								SizeLimit string `yaml:"sizeLimit"`
							} `yaml:"emptyDir"`
						} `yaml:"volumes"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatal(err)
		}
		if obj.Kind != "Deployment" || obj.Metadata.Name != "swiss" {
			continue
		}
		for _, v := range obj.Spec.Template.Spec.Volumes {
			if v.Name != "data" {
				continue
			}
			if v.PersistentVolumeClaim != nil {
				claim = v.PersistentVolumeClaim.ClaimName
			}
			if v.EmptyDir != nil {
				emptyDir = true
				sizeLimit = v.EmptyDir.SizeLimit
			}
			return
		}
		t.Fatal("swissd deployment has no data volume")
	}
	t.Fatal("no swissd deployment")
	return
}

func hasSwissdPVC(t *testing.T, rendered string) bool {
	t.Helper()
	return slices.Contains(objectIDs(t, rendered, "default"), "PersistentVolumeClaim/default/swiss-data")
}

// Default is a PVC, and the data volume mounts it. The catalog-server claim is
// a different object and stays out of this.
func TestChartPersistenceDefault(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t)
	if err != nil {
		t.Fatal(out)
	}
	if !hasSwissdPVC(t, out) {
		t.Fatal("default render has no swissd PVC")
	}
	claim, emptyDir, _ := dataVol(t, out)
	if claim != "swiss-data" || emptyDir {
		t.Fatalf("data volume = claim %q emptyDir %v, want claim swiss-data", claim, emptyDir)
	}
}

// No claim: swissd still deploys, allowDeploy stays on, and sqlite is an
// emptyDir with no sizeLimit. A restart loses the database; the chart allows it.
func TestChartPersistenceEmptyDir(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t, "persistence.enabled=false")
	if err != nil {
		t.Fatal(out)
	}
	if hasSwissdPVC(t, out) {
		t.Fatal("persistence.enabled=false rendered a swissd PVC")
	}
	if !slices.Contains(objectIDs(t, out, "default"), "Deployment/default/swiss") {
		t.Fatal("swissd deployment was not rendered")
	}
	if !strings.Contains(out, "allowDeploy: true") {
		t.Fatal("allowDeploy was not left on")
	}
	claim, emptyDir, sizeLimit := dataVol(t, out)
	if claim != "" || !emptyDir || sizeLimit != "" {
		t.Fatalf("data volume = claim %q emptyDir %v sizeLimit %q, want an emptyDir with no sizeLimit", claim, emptyDir, sizeLimit)
	}
}

func TestChartPersistenceEmptyDirSizeLimit(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t, "persistence.enabled=false", "persistence.emptyDir.sizeLimit=2Gi")
	if err != nil {
		t.Fatal(out)
	}
	_, emptyDir, sizeLimit := dataVol(t, out)
	if !emptyDir || sizeLimit != "2Gi" {
		t.Fatalf("data volume emptyDir %v sizeLimit %q, want emptyDir sizeLimit 2Gi", emptyDir, sizeLimit)
	}
}

// An existing claim is used as-is, and the chart creates no PVC of its own.
func TestChartPersistenceExistingClaim(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t, "persistence.enabled=false", "persistence.existingClaim=foo")
	if err != nil {
		t.Fatal(out)
	}
	if hasSwissdPVC(t, out) {
		t.Fatal("existingClaim rendered a swissd PVC")
	}
	claim, emptyDir, _ := dataVol(t, out)
	if claim != "foo" || emptyDir {
		t.Fatalf("data volume = claim %q emptyDir %v, want claim foo", claim, emptyDir)
	}
}

func TestChartRejectsTwoReplicas(t *testing.T) {
	if _, err := exec.LookPath("helm"); err != nil {
		t.Skip("helm not on PATH")
	}
	out, err := helmTemplate(t, "replicaCount=2")
	if err == nil {
		t.Fatalf("replicaCount=2 rendered:\n%s", out)
	}
	if !strings.Contains(out, "sqlite is a single writer") {
		t.Fatalf("replica failure should name the single writer:\n%s", out)
	}
}
