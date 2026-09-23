package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aceforeverd/swiss/internal/cluster"
	"github.com/aceforeverd/swiss/web"
)

// TestEmbeddedUIIsServed exercises the real built bundle against the real
// handler. It skips when web/dist is empty, because `go build` must work
// without node installed -- but when a build is present, this is the check that
// the binary actually serves it.
func TestEmbeddedUIIsServed(t *testing.T) {
	f := web.FS()
	if f == nil {
		t.Skip("no web build embedded; run `npm run build` in web/")
	}
	cfg := testConfig("prod-b300")
	s := New(cfg, liveProbe(), discardLogger(), "test")
	s.SetWeb(f)
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/catalog/glm-5.3")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || !strings.Contains(string(body), `id="root"`) {
		t.Fatalf("status %d, body %.200s", resp.StatusCode, body)
	}
	// The bundle it names must be fetchable, or the page loads and renders
	// nothing.
	i := strings.Index(string(body), "/assets/")
	if i < 0 {
		t.Fatal("index.html references no bundle")
	}
	ref := string(body)[i:]
	ref = ref[:strings.IndexAny(ref, `"'`)]
	assetResp, err := http.Get(srv.URL + ref)
	if err != nil {
		t.Fatal(err)
	}
	defer assetResp.Body.Close()
	if assetResp.StatusCode != 200 {
		t.Fatalf("bundle %s: status %d", ref, assetResp.StatusCode)
	}
}

func liveProbe() cluster.Fake {
	p := fakeProbe()
	p.Rel = []cluster.Release{
		{Name: "by-hand", Namespace: "modelforge", Chart: "sglang-0.8.0", Status: "deployed", Revision: 1},
		{Name: "glm-53", Namespace: "modelforge", Chart: "sglang-0.8.0", Status: "deployed", Revision: 4,
			SwissFiles: map[string]string{"plan.yaml": "source:\n  model: modelforge\n  variant: sglang-tp2\n"}},
	}
	p.Nod = []cluster.Node{
		{Name: "gpu-1", GPUProduct: "NVIDIA-B300-SXM6-AC", GPUs: 8, Schedulable: true},
	}
	return p
}

// The deploy page is served for a model whose name carries dots, and the API it
// calls reports whether this swissd can deploy at all.
func TestDeployRouteAndAllowDeployFlag(t *testing.T) {
	f := web.FS()
	if f == nil {
		t.Skip("no web build embedded")
	}
	for _, allow := range []bool{false, true} {
		cfg := testConfig("prod-b300")
		cfg.Server.AllowDeploy = allow
		s := New(cfg, liveProbe(), discardLogger(), "test")
		s.SetWeb(f)
		srv := httptest.NewServer(s.Handler())

		resp, err := http.Get(srv.URL + "/deploy/modelforge?variant=sglang-tp2")
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), `id="root"`) {
			t.Errorf("allowDeploy=%v: status %d", allow, resp.StatusCode)
		}

		var info map[string]any
		r2, err := http.Get(srv.URL + "/api/cluster")
		if err != nil {
			t.Fatal(err)
		}
		_ = json.NewDecoder(r2.Body).Decode(&info)
		r2.Body.Close()
		if info["allowDeploy"] != allow {
			t.Errorf("allowDeploy=%v but api reports %v", allow, info["allowDeploy"])
		}
		srv.Close()
	}
}
