package server

import (
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

func TestCanaryArmReadMetadata(t *testing.T) {
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
}

func TestCanaryAssignedArmReadMetadata(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := llmObject("models", "demo-existing-arm", map[string]string{
		llmsvc.AnnProfile:    "prod",
		llmsvc.AssignmentKey: `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"models","name":"demo","uid":"parent-uid","role":"arm","arm":"candidate","configDigest":"sha256:abc","configVersion":"3"}`,
	})
	u.SetUID("arm-uid")
	u.SetResourceVersion("19")
	u.Object["status"] = map[string]any{"phase": "Applied", "helm": map[string]any{"revision": int64(1), "status": "deployed"}}
	if _, err := rig.c.Create(t.Context(), u); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/deployments", "/api/releases/models/demo-existing-arm/status"} {
		code, body := get(t, rig.srv, path)
		if code != 200 {
			t.Fatalf("%s: %d %v", path, code, body)
		}
		if path == "/api/deployments" {
			body = body["deployments"].([]any)[0].(map[string]any)
		}
		if body["uid"] != "arm-uid" {
			t.Fatalf("missing service UID: %v", body)
		}
		assigned, _ := body["assignedTo"].(map[string]any)
		if assigned["name"] != "demo" || assigned["uid"] != "parent-uid" || assigned["role"] != "arm" || assigned["arm"] != "candidate" || assigned["verified"] != true {
			t.Fatalf("unexpected assignedTo: %v", body)
		}
	}
}

func TestCanaryMetadataKeepsNormalServiceOperations(t *testing.T) {
	for _, kind := range []string{"owned", "assigned", "malformed", "orphaned"} {
		for _, operation := range []string{"apply", "install", "rollback", "uninstall", "slo read", "slo write", "slo reset"} {
			t.Run(kind+"/"+operation, func(t *testing.T) {
				statuses := make([]int, 0, 2)
				for _, marked := range []bool{false, true} {
					var sent map[string]any
					up := sloStub(t, true, true, &sent)
					probe := llmProbe()
					probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": probe.Maps["swiss/site-profile"]["profile.yaml"] + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"}
					probe.Secrets["llm-scaler/slo-api"] = map[string]string{"token": "slo-secret"}
					rig := startRig(t, probe, true, true, backendLLMSVC)
					u := ownedArm()
					u.SetName("glm")
					if !marked || kind != "owned" {
						u.SetOwnerReferences(nil)
						u.SetLabels(nil)
					}
					if marked {
						switch kind {
						case "assigned":
							anns := u.GetAnnotations()
							anns[llmsvc.AssignmentKey] = `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"models","name":"demo","uid":"parent-uid","role":"arm","arm":"baseline"}`
							u.SetAnnotations(anns)
						case "malformed":
							anns := u.GetAnnotations()
							anns[llmsvc.AssignmentKey] = "{broken"
							u.SetAnnotations(anns)
						case "orphaned":
							u.SetLabels(map[string]string{llmsvc.CanaryPrefix + "canary-name": "demo"})
						}
					}
					if _, err := rig.c.Create(t.Context(), u); err != nil {
						t.Fatal(err)
					}
					hash := rig.storePlan(t, map[string]any{"release": "glm"})
					var code int
					var body map[string]any
					switch operation {
					case "apply":
						code, body = post(t, rig.srv, "/api/apply", map[string]any{"planHash": hash, "expectRevision": 1})
					case "install":
						code, body = post(t, rig.srv, "/api/install", map[string]any{"planHash": hash})
					case "rollback":
						code, body = post(t, rig.srv, "/api/releases/models/glm/rollback", map[string]any{"toRevision": 1, "expectRevision": 1})
					case "uninstall":
						code, body = del(t, rig.srv, "/api/releases/models/glm")
					case "slo read":
						code, body = get(t, rig.srv, "/api/releases/models/glm/slo")
					case "slo write":
						code, body = put(t, rig.srv, "/api/releases/models/glm/slo", map[string]any{"priority": 10})
					case "slo reset":
						code, body = del(t, rig.srv, "/api/releases/models/glm/slo")
					}
					if body["code"] == "CANARY_MANAGED_SERVICE" || body["code"] == "CANARY_ASSIGNED_SERVICE" {
						t.Fatalf("Canary metadata blocked operation: %d %v", code, body)
					}
					if operation == "apply" || operation == "uninstall" || operation == "slo read" || operation == "slo write" || operation == "slo reset" {
						if code < 200 || code >= 300 {
							t.Fatalf("marked=%v: %d %v", marked, code, body)
						}
					}
					statuses = append(statuses, code)
				}
				if statuses[0] != statuses[1] {
					t.Fatalf("ordinary=%d marked=%d", statuses[0], statuses[1])
				}
			})
		}
	}
}

func TestCanaryRelationshipsFollowLiveMetadata(t *testing.T) {
	rig := startRig(t, llmProbe(), true, false, backendLLMSVC)
	u := ownedArm()
	u.SetLabels(nil)
	u.SetOwnerReferences(nil)
	live, err := rig.c.Create(t.Context(), u)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []struct {
		state      string
		assignment bool
		ownership  bool
	}{
		{"standalone", false, false},
		{"referenced", true, false},
		{"owned", true, true},
		{"referenced", true, false},
		{"standalone", false, false},
	} {
		labels := map[string]string{}
		if step.ownership {
			labels[llmsvc.CanaryPrefix+"canary-name"] = "demo"
		}
		live.SetLabels(labels)
		anns := live.GetAnnotations()
		if step.assignment {
			anns[llmsvc.AssignmentKey] = "{incomplete"
		} else {
			delete(anns, llmsvc.AssignmentKey)
		}
		live.SetAnnotations(anns)
		live, err = rig.c.Update(t.Context(), live)
		if err != nil {
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
			if body["canaryState"] != step.state {
				t.Fatalf("%s: want %s, got %v", path, step.state, body)
			}
			if step.state == "owned" && body["managedBy"].(map[string]any)["verified"] != false {
				t.Fatal("incomplete ownership was verified")
			}
			if step.state == "referenced" && body["assignedTo"].(map[string]any)["verified"] != false {
				t.Fatal("malformed assignment was verified")
			}
		}
	}
}
