package server

import (
	"net/http"
	"testing"

	"github.com/modelsphere/swiss/internal/llmsvc"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8stesting "k8s.io/client-go/testing"
)

func ownedArm() *unstructured.Unstructured {
	u := llmObject("models", "demo-baseline", map[string]string{llmsvc.AnnProfile: "prod"})
	u.SetUID("child-uid")
	u.SetResourceVersion("17")
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "canary.modelsphere.dev/v1alpha1", Kind: "LLMCanary", Name: "demo", UID: "parent-uid", Controller: &controller}})
	u.SetLabels(map[string]string{llmsvc.CanaryPrefix + "canary-name": "demo", llmsvc.CanaryPrefix + "canary-namespace": "models", llmsvc.CanaryPrefix + "canary-uid": "parent-uid"})
	u.Object["status"] = map[string]any{"phase": "Applied", "helm": map[string]any{"revision": int64(1), "status": "deployed"}}
	return u
}

func TestLLMDeleteUsesObservedIdentity(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := llmObject("models", "ordinary", nil)
	u.SetUID("original-child")
	u.SetResourceVersion("17")
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	code, body := del(t, rig.srv, "/api/releases/models/ordinary")
	if code != 200 {
		t.Fatalf("ordinary service: %d %v", code, body)
	}
	found := false
	for _, action := range rig.dyn.Actions() {
		if action.GetVerb() != "delete" {
			continue
		}
		found = true
		pre := action.(k8stesting.DeleteAction).GetDeleteOptions().Preconditions
		if pre == nil || pre.UID == nil || *pre.UID != "original-child" || pre.ResourceVersion == nil || *pre.ResourceVersion != "17" {
			t.Fatalf("delete did not protect identity: %+v", pre)
		}
	}
	if !found {
		t.Fatal("ordinary service was not deleted")
	}
}

func TestCanaryArmReadAndWriteBoundaries(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := ownedArm()
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/deployments", "/api/releases/models/demo-baseline/status"} {
		code, body := get(t, rig.srv, path)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, body)
		}
		if path == "/api/deployments" {
			body = body["deployments"].([]any)[0].(map[string]any)
		}
		owner, _ := body["managedBy"].(map[string]any)
		if owner["name"] != "demo" || owner["uid"] != "parent-uid" || owner["verified"] != true {
			t.Fatal(body)
		}
	}
	hash := rig.storePlan(t, map[string]any{"release": "demo-baseline"})
	rig.dyn.ClearActions()
	checks := []struct {
		path string
		call func() (int, map[string]any)
	}{
		{"apply", func() (int, map[string]any) {
			return post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 1, "forceConflicts": true})
		}},
		{"install", func() (int, map[string]any) {
			return post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
		}},
		{"rollback", func() (int, map[string]any) {
			return post(t, rig.srv, "/api/releases/models/demo-baseline/rollback", map[string]any{"toRevision": 1, "expectRevision": 1})
		}},
		{"uninstall", func() (int, map[string]any) { return del(t, rig.srv, "/api/releases/models/demo-baseline") }},
		{"slo write", func() (int, map[string]any) {
			return put(t, rig.srv, "/api/releases/models/demo-baseline/slo", map[string]any{"priority": 10})
		}},
		{"slo reset", func() (int, map[string]any) { return del(t, rig.srv, "/api/releases/models/demo-baseline/slo") }},
		{"slo read", func() (int, map[string]any) { return get(t, rig.srv, "/api/releases/models/demo-baseline/slo") }},
	}
	for _, check := range checks {
		t.Run(check.path, func(t *testing.T) {
			code, body := check.call()
			if code != http.StatusConflict || body["code"] != "CANARY_MANAGED_SERVICE" {
				t.Fatalf("%d %v", code, body)
			}
			owner, _ := body["owner"].(map[string]any)
			if owner["uid"] != "parent-uid" {
				t.Fatal(body)
			}
		})
	}
	for _, a := range rig.dyn.Actions() {
		if a.GetVerb() == "delete" || a.GetVerb() == "update" || a.GetVerb() == "patch" || a.GetVerb() == "create" {
			t.Fatalf("blocked write reached Kubernetes: %v", a)
		}
	}
}
