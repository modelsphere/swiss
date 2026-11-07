package server

import (
	"github.com/modelsphere/swiss/internal/exec"
	"github.com/modelsphere/swiss/internal/plan"
	"github.com/modelsphere/swiss/internal/values"
)

type planRequest struct {
	// FromRelease recomposes a deployed release: its stored plan supplies the
	// form layer, the model and the variant, and only the catalog layer moves.
	FromRelease string `json:"fromRelease,omitempty"`
	ReuseSource bool   `json:"reuseSource,omitempty"`
	// Catalog names the configured catalog to compose from. Required when the
	// site lists several. An upgrade naming none stays on the one its release
	// came from; naming another moves the release there, same model id.
	Catalog   string `json:"catalog,omitempty"`
	Model     string `json:"model"`
	Version   string `json:"version,omitempty"`
	Variant   string `json:"variant,omitempty"`
	Release   string `json:"release,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	// ServiceID is the identity modelRoute, sloRequirement and the scaler all
	// key off, and LocalPath overrides the site's path template. Both are form
	// values; they are named here rather than left to Overrides because a
	// deploy form should not have to know the key path.
	ServiceID string `json:"serviceId,omitempty"`
	LocalPath string `json:"localPath,omitempty"`
	// GPUProducts narrows the deploy to these accelerator products, any one of
	// which will do. Named here rather than left to Overrides because the node
	// label they match on depends on the variant's vendor, which is the
	// catalog's business and not the form's.
	GPUProducts []string `json:"gpuProducts,omitempty"`
	// ForceGPUProducts accepts gpuProducts the variant's catalog entry does not
	// list, provided each one is on this cluster under the resource the variant's
	// own products resolve to.
	// The variant's GPU count, image and values are used unchanged.
	ForceGPUProducts bool `json:"forceGpuProducts,omitempty"`
	// GPUResource picks the extended resource to request among those the site
	// profile's nodes.gpus lists for the variant's vendor, in place of the one
	// its products' nodes report or the vendor's default. An upgrade sending
	// no form keeps the resource the release runs on.
	GPUResource string      `json:"gpuResource,omitempty"`
	Overrides   values.Tree `json:"overrides,omitempty"`
	// Present, including {}, replaces retained user form/edit values.
	UserValues *values.Tree `json:"userValues,omitempty"`
	// OverridesYAML is the advanced section: a values fragment typed by hand.
	// Parsed here rather than in the browser so there is one parser, and the
	// ownership check still decides what it may contain.
	OverridesYAML string `json:"overridesYAML,omitempty"`
	// EditsYAML is the plan editor: applied after every layer, exempt from
	// ownership, and labelled "edit" wherever the plan is shown.
	EditsYAML string `json:"editsYAML,omitempty"`
	// CreateNamespace composes helmfile's createNamespace into the plan, on top
	// of the site profile's own setting. helm is what creates the namespace;
	// this is where the plan says so.
	//
	// Deprecated: applyFlags.createNamespace on the install.
	CreateNamespace bool `json:"createNamespace,omitempty"`
	// ChartVersion must fall in the variant's chart.version. Empty resolves as
	// chart.Resolve does.
	ChartVersion string `json:"chartVersion,omitempty"`

	// Set by carryForward, never by the caller.
	runningChart plan.ChartRef
	movedFrom    *plan.Plan
	// autoscale is the live replicaCount layer, copied while the previous plan
	// is autoscaling and the new form keeps the rule. Unexported so a request
	// body cannot set it.
	autoscale    values.Tree
	canarySystem values.Tree
}

type applyRequest struct {
	PlanHash string `json:"planHash"`
	// ExpectRevision is the live helm revision the diff was computed against.
	// It is the whole of the optimistic lock: the revision is helm's, held in
	// the cluster, so two operators who diffed the same release cannot both
	// apply. A version column in swissd's own database would have been a lock
	// on a copy rather than on the thing being changed.
	//
	// Zero -- absent -- asserts nothing, which is what an apply that skipped the
	// diff sends. The diff is optional, so the lock it carries is optional with
	// it; the caller is giving up the concurrency check, not evading one.
	ExpectRevision int `json:"expectRevision,omitempty"`
	// Note is why this is being applied, in the operator's own words. Optional,
	// recorded in the audit log and beside the release, and read by nothing --
	// a diff says what changed, and only a person can say why.
	Note string `json:"note,omitempty"`
	// ForceConflicts lets the upgrade take fields another manager owns, which
	// is what a hand `kubectl edit` leaves behind. Per apply, never stored in
	// the plan -- see exec.ApplyOptions.
	//
	// Deprecated: applyFlags.forceConflicts, which wins when sent.
	ForceConflicts bool       `json:"forceConflicts,omitempty"`
	ApplyFlags     applyFlags `json:"applyFlags,omitempty"`
	// ApplyWith is a new install's backend, helm or llmsvc; empty is the site
	// profile's. An existing release keeps its own.
	ApplyWith string `json:"applyWith,omitempty"`
}

// applyFlags are per-request options for diff, install and apply, never stored
// in the plan. Each endpoint takes the ones that apply to it and ignores the rest.
type applyFlags struct {
	SkipSchemaValidation bool `json:"skipSchemaValidation,omitempty"`
	// A pointer so that a false sent here overrides the deprecated top-level
	// forceConflicts.
	ForceConflicts  *bool `json:"forceConflicts,omitempty"`
	CreateNamespace bool  `json:"createNamespace,omitempty"`
}

// opts is what an install (or, without createNamespace, an upgrade) takes from
// the request.
func (req applyRequest) opts(mode exec.Mode) applyOpts {
	o := applyOpts{
		ExpectRevision:       req.ExpectRevision,
		Action:               actionName(mode),
		Note:                 req.Note,
		ForceConflicts:       req.ForceConflicts,
		SkipSchemaValidation: req.ApplyFlags.SkipSchemaValidation,
	}
	if f := req.ApplyFlags.ForceConflicts; f != nil {
		o.ForceConflicts = *f
	}
	// An upgrade's namespace exists already.
	if mode == exec.Install {
		o.CreateNamespace = req.ApplyFlags.CreateNamespace
	}
	return o
}
