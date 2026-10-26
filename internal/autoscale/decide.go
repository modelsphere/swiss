// Package autoscale decides a prefill/decode slice count. It does not touch
// a cluster: the server applies whatever Decide returns.
package autoscale

import "strings"

const (
	HoldApplyInProgress = "apply in progress"
	HoldHelmPending     = "helm pending"
	HoldPlanApplying    = "plan applying"
	HoldLLMApplying     = "llmservice applying"
	HoldLLMPending      = "llmservice pending"
	HoldLLMObserved     = "llmservice observedGeneration behind"
	HoldNotPD           = "not disaggregated"
	HoldDSMissing       = "disaggregatedset missing"
	HoldDSObserved      = "disaggregatedset observedGeneration behind"
	HoldRoleNotReady    = "role not ready"
	HoldRoleNotUpdated  = "role not updated"
	HoldRouterNotReady  = "router not ready"
	// HoldReleaseFailed is a failed helm release, plan, or LLMService.
	// The tick holds until an operator applies or rolls back.
	HoldReleaseFailed = "release failed"
	// HoldUninstalling is a release on its way out: the plan is marked
	// uninstalling, or the LLMService is being deleted. Nothing is applied.
	HoldUninstalling = "uninstalling"

	// MinSlices and MaxSlices are the only bounds swiss applies. decision-gen
	// already clamps to the LLMSLORequirement; this is a safety clamp.
	MinSlices = 1
	MaxSlices = 64
)

// Live is the cluster, as the loop saw it this tick.
type Live struct {
	Backend               string   `json:"backend" yaml:"backend"`
	HelmStatus            string   `json:"helmStatus,omitempty" yaml:"helmStatus,omitempty"`
	PlanPhase             string   `json:"planPhase,omitempty" yaml:"planPhase,omitempty"`
	LLMPhase              string   `json:"llmPhase,omitempty" yaml:"llmPhase,omitempty"`
	LLMGeneration         int64    `json:"llmGeneration,omitempty" yaml:"llmGeneration,omitempty"`
	LLMObservedGeneration int64    `json:"llmObservedGeneration,omitempty" yaml:"llmObservedGeneration,omitempty"`
	PD                    bool     `json:"pd" yaml:"pd"`
	DSMissing             bool     `json:"dsMissing,omitempty" yaml:"dsMissing,omitempty"`
	DSGeneration          int64    `json:"dsGeneration,omitempty" yaml:"dsGeneration,omitempty"`
	DSObservedGeneration  int64    `json:"dsObservedGeneration,omitempty" yaml:"dsObservedGeneration,omitempty"`
	DSSlices              *int     `json:"dsSlices,omitempty" yaml:"dsSlices,omitempty"`
	PlanReplicaCount      int      `json:"planReplicaCount" yaml:"planReplicaCount"`
	Roles                 []Role   `json:"roles,omitempty" yaml:"roles,omitempty"`
	Routers               []Router `json:"routers,omitempty" yaml:"routers,omitempty"`
	LockBusy              bool     `json:"lockBusy,omitempty" yaml:"lockBusy,omitempty"`
	// LLMDeleting is an LLMService with a deletionTimestamp or status.phase Deleting.
	LLMDeleting bool `json:"llmDeleting,omitempty" yaml:"llmDeleting,omitempty"`
}

// Role is one DisaggregatedSet roleStatuses entry.
type Role struct {
	Replicas        int `json:"replicas" yaml:"replicas"`
	ReadyReplicas   int `json:"readyReplicas" yaml:"readyReplicas"`
	UpdatedReplicas int `json:"updatedReplicas" yaml:"updatedReplicas"`
}

// Router is one {fullname}-router-s<i> Deployment.
type Router struct {
	Missing            bool  `json:"missing,omitempty" yaml:"missing,omitempty"`
	Generation         int64 `json:"generation,omitempty" yaml:"generation,omitempty"`
	ObservedGeneration int64 `json:"observedGeneration,omitempty" yaml:"observedGeneration,omitempty"`
	SpecReplicas       int   `json:"specReplicas" yaml:"specReplicas"`
	ReadyReplicas      int   `json:"readyReplicas" yaml:"readyReplicas"`
}

// Decision is one reply from decision-gen. OK false is a hold, never a count of 0.
type Decision struct {
	OK       bool   `json:"ok" yaml:"ok"`
	Replicas int    `json:"replicas" yaml:"replicas"`
	Error    string `json:"error,omitempty" yaml:"error,omitempty"`
}

// Result is what the loop should do. Scale means apply Want.
type Result struct {
	Scale      bool
	Current    int
	Want       int
	HoldReason string
	Decision   *int
}

// Decide is pure. A rollout hold always waits. A readiness hold waits only
// when the next count would be higher, so a scale-down is not blocked by a
// role or router that is not ready yet. want is clamp(decision, MinSlices, MaxSlices).
func Decide(live Live, d Decision) Result {
	cur := current(live)
	res := Result{Current: cur}
	if d.OK {
		n := d.Replicas
		res.Decision = &n
	}
	if hold := rolloutHold(live); hold != "" {
		res.HoldReason = hold
		return res
	}
	if !d.OK {
		res.HoldReason = d.Error
		if res.HoldReason == "" {
			res.HoldReason = "no decision"
		}
		return res
	}
	if releaseFailed(live) {
		res.HoldReason = HoldReleaseFailed
		return res
	}
	want := clamp(d.Replicas, MinSlices, MaxSlices)
	if hold := readinessHold(live); hold != "" && want > cur {
		res.HoldReason = hold
		return res
	}
	res.Want = want
	res.Scale = want != cur
	return res
}

func releaseFailed(live Live) bool {
	if live.HelmStatus == "failed" || live.PlanPhase == "failed" {
		return true
	}
	return live.Backend == "llmsvc" && live.LLMPhase == "Failed"
}

func current(live Live) int {
	if live.DSSlices != nil && *live.DSSlices >= 0 {
		return *live.DSSlices
	}
	if live.PlanReplicaCount < 0 {
		return 0
	}
	return live.PlanReplicaCount
}

// rolloutHold is a change still landing. It waits in either direction.
func rolloutHold(live Live) string {
	if live.PlanPhase == "uninstalling" || live.LLMDeleting || live.LLMPhase == "Deleting" {
		return HoldUninstalling
	}
	if live.LockBusy {
		return HoldApplyInProgress
	}
	if strings.HasPrefix(live.HelmStatus, "pending") {
		return HoldHelmPending
	}
	if live.PlanPhase == "applying" {
		return HoldPlanApplying
	}
	if live.Backend == "llmsvc" {
		switch live.LLMPhase {
		case "Applying":
			return HoldLLMApplying
		case "Pending":
			return HoldLLMPending
		}
		if live.LLMObservedGeneration < live.LLMGeneration {
			return HoldLLMObserved
		}
	}
	if !live.PD {
		return HoldNotPD
	}
	if live.DSMissing {
		return HoldDSMissing
	}
	if live.DSObservedGeneration < live.DSGeneration {
		return HoldDSObserved
	}
	return ""
}

// readinessHold is a replica that exists but is not ready. A scale-down does
// not wait for it. Router observedGeneration stays in this class.
func readinessHold(live Live) string {
	for _, r := range live.Roles {
		if r.ReadyReplicas != r.Replicas {
			return HoldRoleNotReady
		}
		if r.UpdatedReplicas != r.Replicas {
			return HoldRoleNotUpdated
		}
	}
	n := current(live)
	if n < 0 {
		n = 0
	}
	for i := 0; i < n; i++ {
		if i >= len(live.Routers) || routerHeld(live.Routers[i]) {
			return HoldRouterNotReady
		}
	}
	return ""
}

func routerHeld(r Router) bool {
	return r.Missing || r.ReadyReplicas != r.SpecReplicas || r.ObservedGeneration < r.Generation
}

func clamp(n, min, max int) int {
	if n < min {
		return min
	}
	if n > max {
		return max
	}
	return n
}
