package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/krateoplatformops/plumbing/kubeutil/rbac"
)

// fakePermissionChecker allows the checks whose "verb group/resource namespace"
// key is in Allowed. It records the checks of the last call.
type fakePermissionChecker struct {
	Allowed map[string]bool
	Short   bool // return one decision fewer than requested

	got   []rbac.UserCanTarget
	calls int
}

func (f *fakePermissionChecker) CanI(_ context.Context, checks []rbac.UserCanTarget) []bool {
	f.calls++
	f.got = checks
	out := make([]bool, len(checks))
	for i, c := range checks {
		out[i] = f.Allowed[c.Verb+" "+c.GroupResource.Group+"/"+c.GroupResource.Resource+" "+c.Namespace]
	}
	if f.Short {
		out = out[:len(out)-1]
	}
	return out
}

func postCanI(t *testing.T, auth PermissionChecker, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/can-i", strings.NewReader(body))
	rec := httptest.NewRecorder()
	CanIHandler(testLogger(), auth, nil).ServeHTTP(rec, req)
	return rec
}

func decodeCanI(t *testing.T, rec *httptest.ResponseRecorder) []canIItem {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var res canIResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return res.Items
}

func TestCanI_MixedKeepsInputOrder(t *testing.T) {
	auth := &fakePermissionChecker{Allowed: map[string]bool{
		"create composition.krateo.io/cmtests b": true,
		"list apps/deployments ":                 true,
	}}
	rec := postCanI(t, auth, `{"checks":[
		{"verb":"create","group":"composition.krateo.io","resource":"cmtests","namespace":"a"},
		{"verb":"create","group":"composition.krateo.io","resource":"cmtests","namespace":"b"},
		{"verb":"delete","group":"","resource":"secrets","namespace":"b"},
		{"verb":"list","group":"apps","resource":"deployments"}]}`)

	got := decodeCanI(t, rec)
	want := []canIItem{
		{canICheck{"create", "composition.krateo.io", "cmtests", "a"}, false},
		{canICheck{"create", "composition.krateo.io", "cmtests", "b"}, true},
		{canICheck{"delete", "", "secrets", "b"}, false},
		{canICheck{"list", "apps", "deployments", ""}, true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	if len(auth.got) != 4 || auth.got[2].GroupResource.Resource != "secrets" || auth.got[3].Namespace != "" {
		t.Fatalf("checker got %+v", auth.got)
	}
}

func TestCanI_ResponseShape(t *testing.T) {
	rec := postCanI(t, &fakePermissionChecker{},
		`{"checks":[{"verb":"get","resource":"pods"}]}`)
	want := `{"items":[{"verb":"get","group":"","resource":"pods","namespace":"","allowed":false}]}`
	if rec.Body.String() != want {
		t.Fatalf("got %s, want %s", rec.Body.String(), want)
	}
}

func TestCanI_MissingDecisionIsDenied(t *testing.T) {
	auth := &fakePermissionChecker{Short: true, Allowed: map[string]bool{"get /pods x": true, "get /pods y": true}}
	rec := postCanI(t, auth,
		`{"checks":[{"verb":"get","resource":"pods","namespace":"x"},{"verb":"get","resource":"pods","namespace":"y"}]}`)

	got := decodeCanI(t, rec)
	if len(got) != 2 || !got[0].Allowed || got[1].Allowed {
		t.Fatalf("expected [allowed, denied], got %+v", got)
	}
}

func TestCanI_MethodNotAllowed(t *testing.T) {
	for _, method := range []string{"GET", "PUT", "DELETE", "PATCH"} {
		req := httptest.NewRequest(method, "/can-i", nil)
		rec := httptest.NewRecorder()
		CanIHandler(testLogger(), &fakePermissionChecker{}, nil).ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("%s: expected 405, got %d", method, rec.Code)
		}
		if rec.Header().Get("Allow") != "POST" {
			t.Fatalf("%s: expected Allow: POST, got %q", method, rec.Header().Get("Allow"))
		}
	}
}

func TestCanI_ValidationErrors(t *testing.T) {
	check := `{"verb":"get","resource":"pods","namespace":"a"}`
	tooMany := `[` + strings.Repeat(check+`,`, maxChecks) + check + `]`
	long := strings.Repeat("a", maxStringParamLen+1)

	cases := map[string]string{
		"empty body":          ``,
		"not json":            `{`,
		"multiple values":     `{"checks":[` + check + `]} {}`,
		"unknown field":       `{"checks":[` + check + `],"user":"x"}`,
		"unknown check field": `{"checks":[{"verb":"get","resource":"pods","name":"p"}]}`,
		"no checks":           `{}`,
		"empty checks":        `{"checks":[]}`,
		"missing verb":        `{"checks":[{"resource":"pods"}]}`,
		"bad verb":            `{"checks":[` + check + `,{"verb":"*","resource":"pods"}]}`,
		"missing resource":    `{"checks":[{"verb":"get"}]}`,
		"long resource":       `{"checks":[{"verb":"get","resource":"` + long + `"}]}`,
		"long namespace":      `{"checks":[{"verb":"get","resource":"pods","namespace":"` + long + `"}]}`,
		"too many":            `{"checks":` + tooMany + `}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			auth := &fakePermissionChecker{}
			rec := postCanI(t, auth, body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
			}
			if auth.calls != 0 {
				t.Fatalf("checker must not be called on invalid input")
			}
		})
	}
}

func TestCanI_AtMaxChecks(t *testing.T) {
	check := `{"verb":"get","resource":"pods","namespace":"a"}`
	checks := `[` + strings.Repeat(check+`,`, maxChecks-1) + check + `]`
	rec := postCanI(t, &fakePermissionChecker{}, `{"checks":`+checks+`}`)
	if got := decodeCanI(t, rec); len(got) != maxChecks {
		t.Fatalf("expected %d items, got %d", maxChecks, len(got))
	}
}
