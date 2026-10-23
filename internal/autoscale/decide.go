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
	HoldConfigInvalid   = "autoscale config invalid"
)

// Config is what the API stores. The loop clamps a decision into [min, max].
type Config struct {
	Enabled   bool `json:"enabled" yaml:"enabled"`
	MinSlices int  `json:"minSlices" yaml:"minSlices"`
	MaxSlices int  `json:"maxSlices" yaml:"maxSlices"`
}

// Status is what the loop last did. Pointers distinguish "unset" from 0.
type Status struct {
	LastTickAt    string `json:"lastTickAt,omitempty" yaml:"lastTickAt,omitempty"`
	LastDecision  *int   `json:"lastDecision,omitempty" yaml:"lastDecision,omitempty"`
	Current       *int   `json:"current,omitempty" yaml:"current,omitempty"`
	Desired       *int   `json:"desired,omitempty" yaml:"desired,omitempty"`
	HoldReason    string `json:"holdReason,omitempty" yaml:"holdReason,omitempty"`
	LastScaleAt   string `json:"lastScaleAt,omitempty" yaml:"lastScaleAt,omitempty"`
	LastScaleFrom *int   `json:"lastScaleFrom,omitempty" yaml:"lastScaleFrom,omitempty"`
	LastScaleTo   *int   `json:"lastScaleTo,omitempty" yaml:"lastScaleTo,omitempty"`
	LastRevision  *int   `json:"lastRevision,omitempty" yaml:"lastRevision,omitempty"`
	LastError     string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
}

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

// Result is what the loop should do. Scale means apply Want. Failed means the
// error belongs in lastError; a guard hold does not.
type Result struct {
	Scale      bool
	Current    int
	Want       int
	HoldReason string
	Decision   *int
	Failed     bool
}

// Decide is pure. The first matching guard wins. A decision error is a hold
// only when no guard already matched. want is clamp(decision, min, max).
func Decide(cfg Config, live Live, d Decision) Result {
	cur := current(live)
	res := Result{Current: cur}
	if d.OK {
		n := d.Replicas
		res.Decision = &n
	}
	if hold := guard(live); hold != "" {
		res.HoldReason = hold
		return res
	}
	if !d.OK {
		res.HoldReason = d.Error
		if res.HoldReason == "" {
			res.HoldReason = "no decision"
		}
		res.Failed = true
		return res
	}
	if cfg.MinSlices < 1 || cfg.MinSlices > cfg.MaxSlices {
		res.HoldReason = HoldConfigInvalid
		res.Failed = true
		return res
	}
	want := clamp(d.Replicas, cfg.MinSlices, cfg.MaxSlices)
	res.Want = want
	res.Scale = want != cur
	return res
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

func guard(live Live) string {
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
