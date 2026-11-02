package server

import (
	"fmt"
	"maps"
	"net/http"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/llmsvc"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	k8stesting "k8s.io/client-go/testing"
)

func TestLLMUninstallConflictRecovery(t *testing.T) {
	for _, tc := range []struct {
		name             string
		status, attempts int
		code             string
	}{
		{"status update", 200, 2, ""},
		{"repeated conflict", 409, 2, ""},
		{"adopted", 409, 1, "CANARY_MANAGED_SERVICE"},
		{"assigned", 409, 1, "CANARY_ASSIGNED_SERVICE"},
		{"replacement", 409, 1, ""},
		{"gone", 404, 1, ""},
		{"read failure", 502, 1, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := llmProbe()
			labels := map[string]map[string]string{}
			seedLLMPlan(t, &probe, labels, &uninstallCase{Namespace: "models", Release: "ordinary"})
			ref := planRef("models", "ordinary")
			before := maps.Clone(probe.Maps[ref])
			rig := startRig(t, probe, true, false, backendLLMSVC)
			base := rig.s.probe.(*cluster.Fake)
			rig.s.SetWriter(&llmMirrorWriter{fakeWriter: rig.w, probe: base, labels: labels})
			u := llmObject("models", "ordinary", nil)
			u.SetUID("original")
			u.SetResourceVersion("17")
			if _, err := rig.c.Create(t.Context(), u); err != nil {
				t.Fatal(err)
			}
			attempts := 0
			gr := schema.GroupResource{Group: llmsvc.Group, Resource: llmsvc.Resource}
			rig.dyn.PrependReactor("delete", "llmservices", func(a k8stesting.Action) (bool, runtime.Object, error) {
				attempts++
				pre := a.(k8stesting.DeleteAction).GetDeleteOptions().Preconditions
				rv := "17"
				if attempts == 2 {
					rv = "18"
				}
				if pre == nil || pre.UID == nil || *pre.UID != "original" || pre.ResourceVersion == nil || *pre.ResourceVersion != rv {
					t.Errorf("attempt %d: unsafe delete preconditions %+v", attempts, pre)
				}
				if attempts > 1 && tc.name != "repeated conflict" {
					return false, nil, nil
				}
				fresh := u.DeepCopy()
				fresh.SetResourceVersion("18")
				fresh.Object["status"] = map[string]any{"phase": "Applied", "observedGeneration": int64(1)}
				switch tc.name {
				case "adopted":
					fresh.SetOwnerReferences(ownedArm().GetOwnerReferences())
				case "assigned":
					fresh.SetAnnotations(map[string]string{llmsvc.AssignmentKey: `{"apiVersion":"canary.modelsphere.dev/v1alpha1","kind":"LLMCanary","namespace":"models","name":"demo","uid":"parent","role":"arm","arm":"baseline"}`})
				case "replacement":
					fresh.SetUID("replacement")
				case "gone":
					if err := rig.dyn.Tracker().Delete(llmsvc.GVR, "models", "ordinary"); err != nil {
						t.Error(err)
					}
					return true, nil, apierrors.NewConflict(gr, "ordinary", fmt.Errorf("stale"))
				case "read failure":
					rig.dyn.PrependReactor("get", "llmservices", func(k8stesting.Action) (bool, runtime.Object, error) {
						return true, nil, apierrors.NewForbidden(gr, "ordinary", fmt.Errorf("access revoked"))
					})
				}
				if err := rig.dyn.Tracker().Update(llmsvc.GVR, fresh, "models"); err != nil {
					t.Error(err)
				}
				return true, nil, apierrors.NewConflict(gr, "ordinary", fmt.Errorf("stale"))
			})
			code, body := del(t, rig.srv, "/api/releases/models/ordinary")
			if code != tc.status || (tc.code != "" && body["code"] != tc.code) {
				t.Fatalf("status %d: %v", code, body)
			}
			if attempts != tc.attempts {
				t.Fatalf("delete attempts %d, want %d", attempts, tc.attempts)
			}
			if tc.status == 200 {
				if runs := rig.runs(t); len(runs) != 1 || runs[0].Error != "" {
					t.Fatalf("successful retry audit: %+v", runs)
				}
				return
			}
			if !maps.Equal(base.Maps[ref], before) {
				t.Fatalf("plan not restored: %v", base.Maps[ref])
			}
			if labels[ref][cluster.LabelPhase] != "" {
				t.Fatalf("phase label not restored: %v", labels[ref])
			}
			if runs := rig.runs(t); len(runs) != 0 {
				t.Fatalf("rejected deletion recorded as uninstall: %+v", runs)
			}
			if rig.s.llmUninstalling("models", "ordinary") {
				t.Fatal("rejected deletion still tracked")
			}
		})
	}
}

func TestSLOReadFallbackWithInvisibleLLMService(t *testing.T) {
	for _, kind := range []string{"forbidden", "discovery"} {
		t.Run(kind, func(t *testing.T) {
			var sent map[string]any
			up := sloStub(t, true, true, &sent)
			g := newGateServer(t, kind, backendHelm, failBin(t, "helm"), failBin(t, "helmfile"))
			probe := llmProbe()
			probe.Maps["swiss/site-profile"] = map[string]string{"profile.yaml": profileYAML + "\nscaler:\n  sloAddress: " + up.URL + "\n  sloTokenSecret: llm-scaler/slo-api\n"}
			probe.Secrets["llm-scaler/slo-api"] = map[string]string{"token": "slo-secret"}
			g.s.probe = probe
			for range 2 {
				code, body := get(t, g.srv, "/api/releases/models/glm/slo")
				if code != http.StatusOK || body["found"] != true {
					t.Fatalf("read fallback %d: %v", code, body)
				}
			}
			for _, call := range []func() (int, map[string]any){
				func() (int, map[string]any) {
					return put(t, g.srv, "/api/releases/models/glm/slo", map[string]any{"priority": 10})
				},
				func() (int, map[string]any) { return del(t, g.srv, "/api/releases/models/glm/slo") },
				func() (int, map[string]any) { return del(t, g.srv, "/api/releases/models/glm") },
				func() (int, map[string]any) {
					return post(t, g.srv, "/api/releases/models/glm/rollback", map[string]any{"toRevision": 1, "expectRevision": 1})
				},
			} {
				if code, body := call(); code != http.StatusBadGateway {
					t.Fatalf("unverified write %d: %v", code, body)
				}
			}
			if sent != nil || len(g.w.written) != 0 {
				t.Fatal("blocked write reached SLO or cluster writer")
			}
			if g.calls.Load() != 1 {
				t.Fatalf("API error not cached: %d calls", g.calls.Load())
			}
		})
	}
}
