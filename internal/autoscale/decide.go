// Package autoscale decides a prefill/decode slice count. It does not touch
// a cluster: the server applies whatever Decide returns.
package autoscale

import (
	"strings"
	"time"
)

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
	// HoldBackoff skips the same count until retryAfter.
	HoldBackoff = "backing off"
	// HoldReleaseFailed is a helm or plan failure this loop did not record.
	// The tick holds instead of applying again.
	HoldReleaseFailed = "release failed"
	// HoldUninstalling is a release on its way out: the plan is marked
	// uninstalling, or the LLMService is being deleted. Nothing is applied.
	HoldUninstalling = "uninstalling"

	backoffBase = time.Minute
	backoffCap  = 30 * time.Minute
)

// Config is what the API stores. The loop clamps a decision into [min, max].
type Config struct {
	Enabled   bool `json:"enabled" yaml:"enabled"`
	MinSlices int  `json:"minSlices" yaml:"minSlices"`
	MaxSlices int  `json:"maxSlices" yaml:"maxSlices"`
}

// Status is what the loop last did. Pointers distinguish "unset" from 0.
type Status struct {
	LastTickAt     string `json:"lastTickAt,omitempty" yaml:"lastTickAt,omitempty"`
	LastDecision   *int   `json:"lastDecision,omitempty" yaml:"lastDecision,omitempty"`
	Current        *int   `json:"current,omitempty" yaml:"current,omitempty"`
	Desired        *int   `json:"desired,omitempty" yaml:"desired,omitempty"`
	HoldReason     string `json:"holdReason,omitempty" yaml:"holdReason,omitempty"`
	LastScaleAt    string `json:"lastScaleAt,omitempty" yaml:"lastScaleAt,omitempty"`
	LastScaleFrom  *int   `json:"lastScaleFrom,omitempty" yaml:"lastScaleFrom,omitempty"`
	LastScaleTo    *int   `json:"lastScaleTo,omitempty" yaml:"lastScaleTo,omitempty"`
	LastRevision   *int   `json:"lastRevision,omitempty" yaml:"lastRevision,omitempty"`
	LastError      string `json:"lastError,omitempty" yaml:"lastError,omitempty"`
	LastFailedWant *int   `json:"lastFailedWant,omitempty" yaml:"lastFailedWant,omitempty"`
	LastFailedAt   string `json:"lastFailedAt,omitempty" yaml:"lastFailedAt,omitempty"`
	Failures       int    `json:"failures,omitempty" yaml:"failures,omitempty"`
	RetryAfter     string `json:"retryAfter,omitempty" yaml:"retryAfter,omitempty"`
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

// Result is what the loop should do. Scale means apply Want. Failed means the
// error belongs in lastError; a guard hold does not. The backoff fields are
// the state to store: Decide updates them when the wanted count changes, and
// NoteApply updates them after the apply.
type Result struct {
	Scale          bool
	Current        int
	Want           int
	HoldReason     string
	Decision       *int
	Failed         bool
	LastFailedWant *int
	LastFailedAt   string
	Failures       int
	RetryAfter     string
}

// ApplyKind is how an autoscale apply ended. Busy and Conflict are not failures.
type ApplyKind int

const (
	ApplyNone ApplyKind = iota
	ApplyOK
	ApplyFailed
	ApplyBusy
	ApplyConflict
)

// Decide is pure. now and prev are the clock and the stored backoff.
// A rollout hold always waits. A readiness hold waits only when the next
// count would be higher, so a scale-down is not blocked by a role or router
// that is not ready yet. want is clamp(decision, min, max).
func Decide(now time.Time, cfg Config, prev Status, live Live, d Decision) Result {
	cur := current(live)
	res := Result{Current: cur}
	if d.OK {
		n := d.Replicas
		res.Decision = &n
	}
	if hold := rolloutHold(live); hold != "" {
		res.HoldReason = hold
		carryBackoff(&res, prev)
		return res
	}
	if !d.OK {
		res.HoldReason = d.Error
		if res.HoldReason == "" {
			res.HoldReason = "no decision"
		}
		res.Failed = true
		carryBackoff(&res, prev)
		return res
	}
	if cfg.MinSlices < 1 || cfg.MinSlices > cfg.MaxSlices {
		res.HoldReason = HoldConfigInvalid
		res.Failed = true
		carryBackoff(&res, prev)
		return res
	}
	want := clamp(d.Replicas, cfg.MinSlices, cfg.MaxSlices)
	changed := prev.LastFailedWant != nil && *prev.LastFailedWant != want
	if changed {
		clearBackoff(&res)
	} else {
		carryBackoff(&res, prev)
	}
	// A failed release with no recorded autoscale failure is somebody else's
	// apply. Hold. One this loop recorded keeps the backoff below instead.
	if releaseFailed(live) && !changed && !ownedFailure(prev, want) {
		res.HoldReason = HoldReleaseFailed
		return res
	}
	if backingOff(now, res, want) {
		res.HoldReason = HoldBackoff
		return res
	}
	if hold := readinessHold(live); hold != "" && want > cur {
		res.HoldReason = hold
		return res
	}
	res.Want = want
	res.Scale = want != cur
	return res
}

// NoteApply updates backoff after the apply Decide asked for. Success clears
// it. A counted failure waits 1m, doubled for each consecutive failure, and
// never longer than 30m. A busy lock and a revision conflict leave it as it was.
func NoteApply(now time.Time, res Result, kind ApplyKind) Result {
	switch kind {
	case ApplyOK:
		clearBackoff(&res)
	case ApplyFailed:
		res.Failures++
		w := res.Want
		res.LastFailedWant = &w
		res.LastFailedAt = now.UTC().Format(time.RFC3339)
		res.RetryAfter = now.UTC().Add(backoffDelay(res.Failures)).Format(time.RFC3339)
	}
	return res
}

func backoffDelay(failures int) time.Duration {
	if failures < 1 {
		failures = 1
	}
	d := backoffBase
	for i := 1; i < failures; i++ {
		if d > backoffCap/2 {
			return backoffCap
		}
		d *= 2
	}
	if d > backoffCap {
		return backoffCap
	}
	return d
}

func carryBackoff(res *Result, prev Status) {
	if prev.LastFailedWant != nil {
		w := *prev.LastFailedWant
		res.LastFailedWant = &w
	}
	res.LastFailedAt = prev.LastFailedAt
	res.Failures = prev.Failures
	res.RetryAfter = prev.RetryAfter
}

func clearBackoff(res *Result) {
	res.LastFailedWant = nil
	res.LastFailedAt = ""
	res.Failures = 0
	res.RetryAfter = ""
}

func ownedFailure(prev Status, want int) bool {
	return prev.LastFailedWant != nil && *prev.LastFailedWant == want
}

func backingOff(now time.Time, res Result, want int) bool {
	if res.LastFailedWant == nil || *res.LastFailedWant != want || res.RetryAfter == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, res.RetryAfter)
	if err != nil {
		return false
	}
	return now.Before(t)
}

func releaseFailed(live Live) bool {
	return live.HelmStatus == "failed" || live.PlanPhase == "failed"
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
