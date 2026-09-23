package values

// Layer names. Order here is merge order, lowest precedence first, and that
// order is the whole rule: a later layer overrides an earlier one, for any key.
//
// There is no per-layer key ownership. There was, and it was a second mechanism
// answering a question precedence already answers -- a check that refused what
// the merge would have handled correctly anyway. What a reader actually needs is
// not a veto but a report, so plan.Shadowed lists the values one layer set and a
// later one overrode.
const (
	LayerChart   = "chart"   // the chart's own values.yaml; never written by us
	LayerCatalog = "catalog" // what makes this model run
	LayerSite    = "site"    // what makes it work here
	LayerDerived = "derived" // computed from catalog x site; a provenance label only
	LayerForm    = "form"    // how much, where, how routed
	LayerEdit    = "edit"    // typed into the plan editor, applied last
)
