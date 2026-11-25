package steps

import "testing"

func TestPlanSummary(t *testing.T) {
	cases := []struct {
		name        string
		json        string
		wantChanged bool
		wantDestroy []string
	}{
		{"no-op only", `{"resource_changes":[{"address":"a","change":{"actions":["no-op"]}}]}`, false, nil},
		{"read only", `{"resource_changes":[{"address":"a","change":{"actions":["read"]}}]}`, false, nil},
		{"create", `{"resource_changes":[{"address":"a","change":{"actions":["create"]}}]}`, true, nil},
		{"in-place update", `{"resource_changes":[{"address":"a","change":{"actions":["update"]}}]}`, true, nil},
		{"plain delete", `{"resource_changes":[{"address":"vm[0]","change":{"actions":["delete"]}}]}`, true, []string{"vm[0]"}},
		{"replace", `{"resource_changes":[{"address":"vm[1]","change":{"actions":["delete","create"]}}]}`, true, []string{"vm[1]"}},
		{"reverse replace", `{"resource_changes":[{"address":"vm[2]","change":{"actions":["create","delete"]}}]}`, true, []string{"vm[2]"}},
		{"empty", `{"resource_changes":[]}`, false, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := planSummary(tc.json)
			if err != nil {
				t.Fatal(err)
			}
			if res.Changed != tc.wantChanged {
				t.Errorf("Changed = %v, want %v", res.Changed, tc.wantChanged)
			}
			if res.Destructive() != (len(tc.wantDestroy) > 0) {
				t.Errorf("Destructive = %v (destroys %v)", res.Destructive(), res.Destroys)
			}
			if len(res.Destroys) != len(tc.wantDestroy) {
				t.Fatalf("Destroys = %v, want %v", res.Destroys, tc.wantDestroy)
			}
			for i := range tc.wantDestroy {
				if res.Destroys[i] != tc.wantDestroy[i] {
					t.Errorf("Destroys[%d] = %q, want %q", i, res.Destroys[i], tc.wantDestroy[i])
				}
			}
		})
	}
}

func TestPlanSummaryBadJSON(t *testing.T) {
	if _, err := planSummary("not json"); err == nil {
		t.Error("invalid JSON accepted")
	}
}
