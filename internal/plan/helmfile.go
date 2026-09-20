package plan

import (
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// repoAlias names the generated repositories entry. The document is ephemeral,
// so the name never escapes the directory it is written into.
const repoAlias = "charts"

type Repository struct {
	Name string `yaml:"name" json:"name"`
	URL  string `yaml:"url" json:"url"`
}

// HelmDefaults mirror the repo's helmfile.yaml. They are not tunable knobs: a
// cold load here is 20-40 minutes, "failed" usually means "still loading", and
// an automatic rollback of that is the expensive wrong answer.
type HelmDefaults struct {
	Wait          bool     `yaml:"wait" json:"wait"`
	Atomic        bool     `yaml:"atomic" json:"atomic"`
	CleanupOnFail bool     `yaml:"cleanupOnFail" json:"cleanupOnFail"`
	CreateNS      bool     `yaml:"createNamespace" json:"createNamespace"`
	HistoryMax    int      `yaml:"historyMax" json:"historyMax"`
	DiffArgs      []string `yaml:"diffArgs" json:"diffArgs"`
}

// HelmfileRelease is the release stanza of the generated document.
type HelmfileRelease struct {
	Name      string   `yaml:"name" json:"name"`
	Namespace string   `yaml:"namespace" json:"namespace"`
	Chart     string   `yaml:"chart" json:"chart"`
	Version   string   `yaml:"version,omitempty" json:"version,omitempty"`
	Values    []string `yaml:"values" json:"values"`
}

type HelmfileDoc struct {
	Repositories []Repository      `yaml:"repositories,omitempty" json:"repositories,omitempty"`
	HelmDefaults HelmDefaults      `yaml:"helmDefaults" json:"helmDefaults"`
	Releases     []HelmfileRelease `yaml:"releases" json:"releases"`
}

func defaults(createNamespace bool) HelmDefaults {
	return HelmDefaults{
		Wait: false, Atomic: false, CleanupOnFail: false,
		CreateNS: createNamespace, HistoryMax: 20,
		DiffArgs: []string{"--three-way-merge"},
	}
}

// HelmfileDocument renders the release declaration. chartRoot is a fallback for
// plans whose profile named neither a repo nor a path.
func (p *Plan) HelmfileDocument(chartRoot string) (*HelmfileDoc, error) {
	chart, version, repo, err := p.chartRef(chartRoot)
	if err != nil {
		return nil, err
	}
	d := &HelmfileDoc{
		HelmDefaults: defaults(p.CreateNamespace),
		Releases: []HelmfileRelease{{
			Name:      p.Release.Name,
			Namespace: p.Release.Namespace,
			Chart:     chart,
			Version:   version,
			Values:    p.ValuesFiles(),
		}},
	}
	if repo != nil {
		d.Repositories = []Repository{*repo}
	}
	return d, nil
}

// RenderHelmfile is HelmfileDocument as YAML.
func (p *Plan) RenderHelmfile(chartRoot string) (string, error) {
	d, err := p.HelmfileDocument(chartRoot)
	if err != nil {
		return "", err
	}
	b, err := yaml.Marshal(d)
	return string(b), err
}

// chartRef resolves where helmfile pulls the chart from.
//
// An OCI registry is addressed directly: oci://host/path/name plus a version.
// A classic HTTP chart repository is not -- helm has to be told it is a repo
// before it can resolve name+version into an archive, so the document declares a
// repositories entry and the release refers to it as alias/name. Passing the
// repo URL as the chart makes helm fetch that URL as an archive, which 404s
// because the archive is <url>/<name>-<version>.tgz.
func (p *Plan) chartRef(chartRoot string) (chart, version string, repo *Repository, err error) {
	switch {
	case strings.HasPrefix(p.Chart.Repo, "oci://"):
		return strings.TrimSuffix(p.Chart.Repo, "/") + "/" + p.Chart.Name, p.Chart.Version, nil, nil
	case p.Chart.Repo != "":
		return repoAlias + "/" + p.Chart.Name, p.Chart.Version,
			&Repository{Name: repoAlias, URL: strings.TrimSuffix(p.Chart.Repo, "/")}, nil
	case p.Chart.Path != "":
		abs, err := filepath.Abs(filepath.Join(p.Chart.Path, p.Chart.Name))
		return abs, "", nil, err
	case chartRoot != "":
		abs, err := filepath.Abs(filepath.Join(chartRoot, p.Chart.Name))
		return abs, "", nil, err
	}
	return "", "", nil, fmt.Errorf("no chart source: set chartRepo or chartPath in the site profile, or pass --chart-root")
}
