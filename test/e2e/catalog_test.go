package e2e

import (
	"context"
	"testing"

	"github.com/modelsphere/swiss/internal/catalog"
)

func TestPinnedMockCatalog(t *testing.T) {
	c, err := catalog.OpenWithSchema(context.Background(), "testdata/catalog", "v2")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := c.Resolve(context.Background(), "mock-llm", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Variant.Requires.GPUs != 0 || !resolved.Variant.Weightless() {
		t.Fatal("e2e fixture must run without GPUs or model weights")
	}
	if resolved.Variant.Image.Repository != "ghcr.io/modelsphere/mock-llmd" || resolved.Variant.Image.Tag != "0.1.0" {
		t.Fatalf("unexpected mock image: %+v", resolved.Variant.Image)
	}
}
