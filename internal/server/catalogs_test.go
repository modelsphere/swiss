package server

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelsphere/swiss/internal/cluster"
	"github.com/modelsphere/swiss/internal/plan"
)

// twoCatalogs is a server whose profile lists two catalogs, "public" at a and
// "internal" at b, both copies of the shipped index so each really opens.
// dflt names the one marked default, or none when empty. rels seeds the
// cluster, given where the catalogs landed.
func twoCatalogs(t *testing.T, dflt string, rels func(a, b string) []cluster.Release) (*httptest.Server, *Server, string, string) {
	t.Helper()
	a, _ := catalogCopy(t)
	b, _ := catalogCopy(t)
	var rel []cluster.Release
	if rels != nil {
		rel = rels(a, b)
	}
	entry := func(name, url string) string {
		e := "  - name: " + name + "\n    url: " + url + "\n"
		if name == dflt {
			e += "    default: true\n"
		}
		return e
	}
	probe := cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n" + entry("public", a) + entry("internal", b)},
		},
		Rel: rel,
	}
	s := New(testConfig("c"), probe, discardLogger(), "test")
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, s, a, b
}

// deployedFrom is release r, whose plan records the catalog at source, when
// that catalog was at ref. name is the catalog's name, which plans from before
// names were recorded do not carry.
func deployedFrom(source, name, ref string) cluster.Release {
	meta := "source:\n  catalog: " + source + "\n  ref: " + ref + "\n"
	if name != "" {
		meta += "  catalogName: " + name + "\n"
	}
	meta += "  model: m\n  variant: v\nrelease:\n  name: r\n  namespace: ns\n"
	return cluster.Release{
		Name: "r", Namespace: "ns", Status: "deployed", Revision: 1,
		SwissFiles: map[string]string{plan.MetaFile: meta},
	}
}

func releases(r ...cluster.Release) func(string, string) []cluster.Release {
	return func(string, string) []cluster.Release { return r }
}

// upgradeCatalog is the catalog an upgrade of r composes from.
func upgradeCatalog(t *testing.T, s *Server) (string, error) {
	t.Helper()
	out, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns"})
	return out.Catalog, err
}

// With several catalogs and no default, naming none is refused rather than
// defaulted: a page or a deploy that lands on whichever catalog is listed
// first is the thing this list exists to prevent.
func TestSeveralCatalogsMustBeNamed(t *testing.T) {
	_, s, _, b := twoCatalogs(t, "", nil)
	if _, err := s.CatalogRepo(t.Context(), ""); err == nil || !strings.Contains(err.Error(), "public, internal") {
		t.Fatalf("an unnamed catalog must be refused, listing the choices: %v", err)
	}
	repo, err := s.CatalogRepo(t.Context(), "internal")
	if err != nil || repo.URL != b {
		t.Fatalf("got %+v, %v; want internal at %s", repo, err, b)
	}
	if _, err := s.CatalogRepo(t.Context(), "nope"); err == nil {
		t.Fatal("an unknown catalog name must be refused")
	}
}

// With a default marked, naming no catalog means that one: a page opens in it
// without asking, and a request that names none composes from it.
func TestUnnamedCatalogIsTheDefault(t *testing.T) {
	srv, s, _, b := twoCatalogs(t, "internal", nil)
	if repo, err := s.CatalogRepo(t.Context(), ""); err != nil || repo.Name != "internal" {
		t.Fatalf("got %+v, %v; want the default, internal", repo, err)
	}
	if code, body := get(t, srv, "/api/catalog"); code != 200 || body["name"] != "internal" {
		t.Fatalf("got %d %v; want the default catalog", code, body)
	}
	_, body := get(t, srv, "/api/cluster")
	cats := body["catalogs"].([]any)
	if cats[0].(map[string]any)["default"] != nil || cats[1].(map[string]any)["default"] != true {
		t.Errorf("catalogs = %v, want internal marked default", cats)
	}
	if body["catalog"] != b {
		t.Errorf("catalog = %v, want the default's url for pages that predate the list", body["catalog"])
	}
}

// One catalog is the catalog: there is nothing to choose.
func TestOneCatalogNeedsNoName(t *testing.T) {
	s := New(testConfig("c"), fakeProbe(), discardLogger(), "test")
	repo, err := s.CatalogRepo(t.Context(), "")
	if err != nil || repo.Name != "default" {
		t.Fatalf("got %+v, %v; want the configured catalog as default", repo, err)
	}
}

func TestCatalogEndpointSelectsByName(t *testing.T) {
	srv, _, _, _ := twoCatalogs(t, "", nil)
	if code, body := get(t, srv, "/api/catalog"); code != 400 {
		t.Fatalf("no ?catalog with two configured and no default: got %d %v, want 400", code, body)
	}
	if code, _ := get(t, srv, "/api/catalog?catalog=nope"); code != 400 {
		t.Fatalf("an unknown catalog is a bad request, not a bad gateway: got %d", code)
	}
	code, body := get(t, srv, "/api/catalog?catalog=internal")
	if code != 200 || body["name"] != "internal" || body["ref"] == "" {
		t.Fatalf("got %d %v, want the internal catalog", code, body)
	}
}

// The cluster endpoint lists every catalog a page may select, with the source
// a plan records, so a page can tell which catalog a release came from.
func TestClusterListsTheCatalogs(t *testing.T) {
	srv, _, a, _ := twoCatalogs(t, "", nil)
	_, body := get(t, srv, "/api/cluster")
	cats, _ := body["catalogs"].([]any)
	if len(cats) != 2 {
		t.Fatalf("catalogs = %v, want two", body["catalogs"])
	}
	first := cats[0].(map[string]any)
	if first["name"] != "public" || first["url"] != a || first["source"] != catalogSource(a) || first["ref"] == "" {
		t.Errorf("first catalog = %v", first)
	}
	if body["catalogFrom"] != "profile" {
		t.Errorf("catalogFrom = %v, want profile", body["catalogFrom"])
	}
}

// A composed plan records the catalog's name beside its location: that is
// what an upgrade stays on.
func TestPlansRecordTheCatalogName(t *testing.T) {
	p, _ := livePlan(t, planRequest{Model: "glm5.1", Release: "r", ServiceID: "r"})
	if p.Source.CatalogName != "default" || p.Source.Catalog == "" {
		t.Errorf("source = %+v, want the catalog's location and its name", p.Source)
	}
}

// An upgrade naming no catalog stays on the one its plan names, whatever the
// default is.
func TestUpgradeStaysOnItsNamedCatalog(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "internal", func(a, _ string) []cluster.Release {
		return []cluster.Release{deployedFrom(catalogSource(a), "public", "sha256:old")}
	})
	if got, err := upgradeCatalog(t, s); err != nil || got != "public" {
		t.Errorf("got %q, %v; want the named catalog, public", got, err)
	}
}

// An upgrade naming another catalog moves the release there, carrying the
// model id and the variant over unchanged.
func TestUpgradeMovesToTheNamedCatalog(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "", func(a, _ string) []cluster.Release {
		return []cluster.Release{deployedFrom(catalogSource(a), "public", "sha256:old")}
	})
	out, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Catalog: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	if out.Catalog != "internal" || out.Model != "m" || out.Variant != "v" {
		t.Errorf("got catalog %q model %q variant %q; want internal, m, v", out.Catalog, out.Model, out.Variant)
	}
}

// Moving needs only the target catalog: the plan records everything about the
// old one, so a release can leave a catalog the site no longer lists.
func TestUpgradeMovesOffACatalogThatIsGone(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "", releases(deployedFrom("https://gone.example.com/index.json", "archived", "sha256:old")))
	out, err := s.carryForward(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Catalog: "public"})
	if err != nil || out.Catalog != "public" {
		t.Errorf("got %q, %v; want public", out.Catalog, err)
	}
}

// movedServer lists one catalog, "internal", at the full catalog checkout --
// not the one doc's plan was composed from, which the site does not list.
func movedServer(t *testing.T, doc map[string]string) *Server {
	t.Helper()
	dir, err := filepath.Abs("../../../swiss-catalog")
	if err != nil {
		t.Fatal(err)
	}
	probe := cluster.Fake{
		Maps: map[string]map[string]string{
			"swiss/site-profile": {"profile.yaml": profileYAML + "catalogs:\n  - name: internal\n    url: " + dir + "\n"},
		},
		Rel: []cluster.Release{{Name: "r", Namespace: "ns", Status: "deployed", Revision: 1, SwissFiles: doc}},
	}
	return New(testConfig("c"), probe, discardLogger(), "test")
}

// The composed plan records the catalog it moved to, under the same model.
func TestUpgradeAcrossCatalogsRecordsTheNewOne(t *testing.T) {
	prev, doc := livePlan(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	s := movedServer(t, doc)
	p, err := s.compose(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Catalog: "internal"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Source.CatalogName != "internal" || p.Source.Model != prev.Source.Model || p.Source.Variant != prev.Source.Variant {
		t.Errorf("source = %+v; want catalog internal, model %s, variant %s", p.Source, prev.Source.Model, prev.Source.Variant)
	}
}

// The target catalog must have the release's variant: the same model id is no
// promise of the same variants, and a different one is a different deploy.
func TestUpgradeAcrossCatalogsNeedsTheVariant(t *testing.T) {
	prev, doc := livePlan(t, planRequest{Model: "qwen3.6-35b-a3b", Release: "r", ServiceID: "r"})
	doc[plan.MetaFile] = strings.Replace(doc[plan.MetaFile], "variant: "+prev.Source.Variant, "variant: gone", 1)
	s := movedServer(t, doc)
	if _, err := s.compose(t.Context(), planRequest{FromRelease: "r", Namespace: "ns", Catalog: "internal"}); err == nil ||
		!strings.Contains(err.Error(), `no variant "gone"`) {
		t.Errorf("got %v, want a refusal naming the missing variant", err)
	}
}

// A plan names a catalog the site no longer lists: refused, not sent to the
// default. It says where it came from, and that is not there.
func TestUpgradeFromANamedCatalogThatIsGoneIsRefused(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "internal", releases(deployedFrom("https://gone.example.com/index.json", "archived", "sha256:old")))
	if _, err := upgradeCatalog(t, s); err == nil || !strings.Contains(err.Error(), `"archived", which this site no longer lists`) {
		t.Errorf("got %v, want a refusal naming the missing catalog", err)
	}
}

// A plan from before names were recorded belongs to the catalog at the
// location it records, when the site lists one there -- default or not.
func TestUnnamedPlanMatchesByLocation(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "internal", func(a, _ string) []cluster.Release {
		return []cluster.Release{deployedFrom(catalogSource(a), "", "sha256:old")}
	})
	if got, err := upgradeCatalog(t, s); err != nil || got != "public" {
		t.Errorf("got %q, %v; want public, whose location the plan records", got, err)
	}
}

// The migration path: a plan that names no catalog and records a location the
// site does not list -- deployed before the list, or from a laptop with the
// CLI -- belongs to the default, so it upgrades without anyone editing it.
func TestUnnamedPlanFallsBackToTheDefault(t *testing.T) {
	for name, source := range map[string]string{
		"unlisted location": "/Users/someone/swiss-catalog/index.json",
		"no location":       "",
	} {
		_, s, _, _ := twoCatalogs(t, "internal", releases(deployedFrom(source, "", "sha256:old")))
		if got, err := upgradeCatalog(t, s); err != nil || got != "internal" {
			t.Errorf("%s: got %q, %v; want the default, internal", name, got, err)
		}
	}
}

// With nothing to fall back on, an unnamed plan the site cannot place is
// refused, and the refusal says how to fix it.
func TestUnnamedPlanWithNoDefaultIsRefused(t *testing.T) {
	_, s, _, _ := twoCatalogs(t, "", releases(deployedFrom("https://gone.example.com/index.json", "", "sha256:old")))
	if _, err := upgradeCatalog(t, s); err == nil || !strings.Contains(err.Error(), "mark one catalog default") {
		t.Errorf("got %v, want a refusal that says to mark a default", err)
	}
}

// Each release is compared with the catalog it belongs to, as that catalog
// reads now, and reported under the catalog's name -- found the way an upgrade
// finds it, so an unnamed plan lands on the default.
func TestDeploymentsCompareWithTheirOwnCatalog(t *testing.T) {
	for name, rel := range map[string]func(a, b string) []cluster.Release{
		"named": releases(deployedFrom("https://moved.example.com/index.json", "internal", "sha256:old")),
		"by location": func(_, b string) []cluster.Release {
			return releases(deployedFrom(catalogSource(b), "", "sha256:old"))("", "")
		},
		"falls to default": releases(deployedFrom("/Users/someone/swiss-catalog/index.json", "", "sha256:old")),
	} {
		srv, _, _, _ := twoCatalogs(t, "internal", rel)
		_, body := get(t, srv, "/api/deployments")
		rows, _ := body["deployments"].([]any)
		if len(rows) != 1 {
			t.Fatalf("%s: deployments = %v, want one", name, body["deployments"])
		}
		row := rows[0].(map[string]any)
		if row["catalog"] != "internal" || row["drift"] != "catalog moved since this was deployed" {
			t.Errorf("%s: row = %v, want catalog internal, behind it", name, row)
		}
		if body["summary"].(map[string]any)["catalogBehind"] != float64(1) {
			t.Errorf("%s: summary = %v, want one behind", name, body["summary"])
		}
		// Two catalogs: no single ref describes the page.
		if body["catalogRef"] != "" {
			t.Errorf("%s: catalogRef = %v, want empty with several catalogs", name, body["catalogRef"])
		}
	}
}
