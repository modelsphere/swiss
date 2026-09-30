package server

import "testing"

// The deploy form's image field is repository:tag, split client-side into the
// two values the chart takes. Both land in the form layer, which merges after
// the catalog's -- so a tag typed here wins over the one the variant pins.
//
// That is the whole reason the field carries a tag: pulling a one-off engine
// build for a single deploy without republishing a catalog version.
func TestFormImageTagWinsOverTheCatalogs(t *testing.T) {
	srv, _ := deployServer(t, true)

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": "r", "serviceId": "r",
		"overrides": map[string]any{
			"image": map[string]any{
				"repository": "registry.example.com:5000/sglang",
				"tag":        "v0.5.19-rc3",
			},
		},
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}

	vals := planValues(body)
	img, _ := vals["image"].(map[string]any)
	if img["tag"] != "v0.5.19-rc3" {
		t.Errorf("the form's tag must win: %v", img)
	}
	// A registry port is not a tag; the repository has to survive intact.
	if img["repository"] != "registry.example.com:5000/sglang" {
		t.Errorf("repository mangled: %v", img)
	}
	if got := layerOf(body, "image.tag"); got != "form" {
		t.Errorf("image.tag should be attributed to the form, got %q", got)
	}
	if got := layerOf(body, "image.repository"); got != "form" {
		t.Errorf("image.repository should be attributed to the form, got %q", got)
	}
}

// The other half: an empty image field leaves the tag where it was, so the
// field carrying a tag now costs nothing when nobody fills it in.
func TestWithoutAFormImageTheCatalogTagStands(t *testing.T) {
	srv, _ := deployServer(t, true)

	code, body := post(t, srv, "/api/plans", map[string]any{
		"model": "qwen3.6-35b-a3b", "release": "r", "serviceId": "r",
	})
	if code != 200 {
		t.Fatalf("status %d: %v", code, body)
	}
	img, _ := planValues(body)["image"].(map[string]any)
	if img["tag"] == nil || img["tag"] == "" {
		t.Errorf("the catalog still pins the tag: %v", img)
	}
	if got := layerOf(body, "image.tag"); got != "catalog" {
		t.Errorf("unoverridden, the tag is the catalog's, got %q", got)
	}
}
