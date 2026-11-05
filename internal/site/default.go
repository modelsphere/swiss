package site

import "strings"

// DefaultYAML is the profile a fresh install starts from, as text.
//
// Text rather than a marshalled struct: this is what the setup form is
// pre-filled with, and the comments are most of what makes the form answerable
// by someone who has not read the type. It parses -- the one required field
// that cannot be guessed, the cluster's own name, is filled in by the caller.
//
// It lives here rather than in the chart's values because swissd owns the
// profile now: two spellings of a default drift, and the one in a values file
// cannot be shown to somebody filling in a form.
func DefaultYAML(cluster string) string {
	if cluster == "" {
		cluster = "this-cluster"
	}
	return strings.NewReplacer("{{cluster}}", cluster, "{{gpus}}", indent(defaultGPUs, "    ")).Replace(defaultTemplate)
}

// defaultGPUs is the nodes.gpus a new profile starts with, and what swissd adds
// once to a stored profile from before nodes.gpus. Nothing reads it otherwise:
// the profile is the only GPU table.
const defaultGPUs = `- vendor: nvidia
  resource: nvidia.com/gpu
  labelKey: nvidia.com/gpu.product
- vendor: ascend
  resource: huawei.com/Ascend910
  labelKey: accelerator/huawei-ascend910
- vendor: cambricon
  resource: cambricon.com/mlu
  labelKey: cambricon.com/mlu.product
- vendor: hygon
  resource: hygon.com/dcu
  labelKey: hygon.com/dcu.product
- vendor: amd
  resource: amd.com/gpu
  labelKey: amd.com/gpu.device-id
`

// gpusBlock is defaultGPUs as it sits in a profile, for an operator adding it
// by hand.
func gpusBlock() string { return "nodes:\n  gpus:\n" + indent(defaultGPUs, "    ") }

func indent(s, by string) string {
	return by + strings.ReplaceAll(strings.TrimSuffix(s, "\n"), "\n", "\n"+by) + "\n"
}

const defaultTemplate = `# The site profile: what makes a model work in THIS cluster. It describes this
# cluster, so it lives in this cluster -- the public catalog cannot know any of
# it, and a deploy form should not have to retype it.
name: {{cluster}}

# Where releases land unless a deploy overrides it.
namespace: models

# Backend a new install uses when the deploy names none. Omit, or helm, and
# swissd runs helmfile. llmsvc creates an LLMService and the operator runs helm;
# it needs the CRD. A release already in the cluster stays on the backend that
# owns it.
# applyWith: helm

# The model catalogs this site deploys from, each an https base or an absolute
# path under a name the pages select by. Viewing the catalog or deploying picks
# one; with a single catalog there is nothing to pick. Set here they win over
# swissd's own config file, so catalogs can be added from the web without a
# helm upgrade. Left out, the configured default stands -- which is also what
# the CLI uses, since it cannot read this ConfigMap.
# With several, the one marked default is used unless a page names another.
# catalogs:
#   - name: public
#     url: https://modelsphere.github.io/model-catalog/
#     default: true

# Where charts are pulled from -- the registry the catalog deliberately does
# not name. An https helm repo or an oci:// registry, e.g. a mirror at
# oci://ghcr.io/<org>/charts.
chartRepo: https://modelsphere.github.io/helm-charts

registry:
  # Replaces the registry host/org of an engine image. The catalog pins which
  # build; this says where it is pulled from, e.g. docker.io/<org> or
  # ghcr.io/<org>. Empty uses the catalog's repository as-is.
  mirror: ""

model:
  # model.localPath, built from the catalog's source.hf. A template rather than
  # a per-model table: a site that has to add a row for every model in a public
  # catalog will not keep up with it. {{hf}}, {{org}}, {{name}} and {{model}}
  # expand.
  pathTemplate: /mnt/disk0/models/{{name}}

cache:
  # Off emits nothing under cache: -- the section does not exist in every chart
  # version, and a values file carrying a key the chart has never heard of is
  # rejected by its schema rather than ignored.
  enabled: false
  # hostPath: /mnt/disk0/sglang-cache

scaler:
  # What the chart receives.
  serverAddress: http://decision-gen.llm-scaler.svc:80
  # Same service. swissd calls this to read and edit LLMSLORequirement
  # thresholds after install. It is not copied into chart values.
  sloAddress: http://slo-api.llm-scaler.svc:80
  # Bearer token for that API. The profile names the Secret; the token is
  # not written here. Key defaults to "token". A bare name is swissd's namespace.
  # sloTokenSecret: llm-scaler/slo-api
  # sloTokenKey: token

# swissd's PD slice autoscaler, for releases planned with PD on and the form
# SLO on. Field names follow the LLMScaler's.
# pdScale:
#   # How often swissd asks decision-gen for a slice count. Unset is 60.
#   syncPeriodSeconds: 60
#   # How long a hold may defer a scale before it is applied anyway: a
#   # DisaggregatedSet rollout that never lands, a failed release, pods that
#   # never get ready. Uninstalling, a missing DisaggregatedSet and an apply
#   # in flight still hold. Unset is 600; 0 defers indefinitely.
#   deferTimeoutSeconds: 600

# The site's preferences, beneath the catalog: a variant that sets one of these
# overrides it, and a deploy can still choose either way. Every other section
# here is a fact about this cluster and wins over the variant.
# defaults:
#   # What a deploy starts each feature at; unset is on. Routing is not here:
#   # it is route.enabled, which no variant can override.
#   features:
#     cart: true
#     serviceMonitor: true
#     sloRequirement: true
#     scaler: true

route:
  # Whether a deploy publishes a route on openresty unless it says otherwise.
  # A site fact, so a catalog variant cannot turn it on; unset is off.
  enabled: false
  # Where callers reach openresty from outside, e.g. https://llm.example.com.
  # Display only: every check swissd makes still goes to the in-cluster
  # service, so this can be absent without breaking anything but the link.
  # gateway: ""
  nginxConfigMap: llm-route/openresty-conf
  monitorConfigMap: llm-route/monitor-conf
  # How swissd authenticates when the serving and health checks call the
  # entrypoint. This profile is a ConfigMap, so it names a Secret rather than
  # holding the key. A bare name is swissd's own namespace.
  # auth:
  #   secretRef: llm-openresty
  #   secretKey: apiKey

nodes:
  # A full GPU node, used to derive cache.maxSlotsPerNode.
  gpusPerNode: 8
  # GPU resources this cluster's nodes advertise; swissd knows no others. A
  # vendor's first entry is what its pods request when no node names one.
  # labelKey is the node label telling products apart under the resource, and
  # products name the catalog's product where a label value differs from it
  # (the product name itself always matches too):
  #     products:
  #       - product: Ascend-910B3
  #         values: [module-910b-8]
  gpus:
{{gpus}}
# The other clusters' swissd instances, for the nav switcher.
# sites:
#   - name: prod-h100
#     url: https://swiss.h100.internal
`
