package plan

import (
	"testing"

	"github.com/modelsphere/swiss/internal/values"
)

func TestNamedRouteOf(t *testing.T) {
	for _, tc := range []struct {
		name  string
		vals  values.Tree
		route string
		named bool
	}{
		{"route wins", values.Tree{"serviceId": "s", "modelRoute": values.Tree{"nginx": values.Tree{"route": "r"}}}, "r", true},
		{"serviceId", values.Tree{"serviceId": "s"}, "s", true},
		{"release fallback", values.Tree{}, "rel", false},
		{"a non-string names nothing", values.Tree{"serviceId": 123}, "rel", false},
		{"an empty route falls through", values.Tree{"serviceId": "s", "modelRoute": values.Tree{"nginx": values.Tree{"route": ""}}}, "s", true},
		{"routing off", values.Tree{"serviceId": "s", "modelRoute": values.Tree{"enabled": false}}, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			route, named := NamedRouteOf(tc.vals, "rel")
			if route != tc.route || named != tc.named {
				t.Fatalf("got (%q, %v), want (%q, %v)", route, named, tc.route, tc.named)
			}
			if RouteOf(tc.vals, "rel") != tc.route {
				t.Fatalf("RouteOf disagrees with NamedRouteOf")
			}
		})
	}
}
