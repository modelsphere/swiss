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
	return strings.ReplaceAll(defaultTemplate, "{{cluster}}", cluster)
}

const defaultTemplate = `# The site profile: what makes a model work in THIS cluster. It describes this
# cluster, so it lives in this cluster -- the public catalog cannot know any of
# it, and a deploy form should not have to retype it.
name: {{cluster}}

# Where releases land unless a deploy overrides it.
namespace: modelforge

# Where the model catalog is read from: an https base, or an absolute path.
# Set here it wins over swissd's own config file, so the catalog can be
# repointed from the web without a helm upgrade. Left out, the configured
# default stands -- which is also what the CLI uses, since it cannot read this
# ConfigMap.
# catalog: https://models.example.com/swiss-catalog/

# Where charts are pulled from -- the registry the catalog deliberately does
# not name.
# chartRepo: oci://harbor.example.com/charts

registry:
  # Replaces the registry host/org of an engine image. The catalog pins which
  # build; this says where it is pulled from. Empty uses the catalog's
  # repository as-is.
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

route:
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

# The other clusters' swissd instances, for the nav switcher.
# sites:
#   - name: prod-h100
#     url: https://swiss.h100.internal
`
