package rbac

import (
	"context"
	"testing"

	plumbingRbac "github.com/krateoplatformops/plumbing/kubeutil/rbac"
	"github.com/krateoplatformops/resources-presenter/internal/sql"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// --- Unit tests for buildUserCanTargets ---

func TestBuildUserCanTargets_Empty(t *testing.T) {
	got := buildUserCanTargets(nil)
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %d", len(got))
	}
}

func TestBuildUserCanTargets_Conversion(t *testing.T) {
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "prod"},
		{Group: "widgets.templates.krateo.io", Resource: "panels", Namespace: "krateo-system"},
	}

	got := buildUserCanTargets(targets)

	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(got))
	}

	// Verify first target.
	if got[0].Verb != "get" {
		t.Errorf("expected verb=get, got %q", got[0].Verb)
	}
	if got[0].GroupResource.Group != "apps" {
		t.Errorf("expected group=apps, got %q", got[0].GroupResource.Group)
	}
	if got[0].GroupResource.Resource != "deployments" {
		t.Errorf("expected resource=deployments, got %q", got[0].GroupResource.Resource)
	}
	if got[0].Namespace != "prod" {
		t.Errorf("expected namespace=prod, got %q", got[0].Namespace)
	}

	// Verify second target.
	if got[1].Verb != "get" {
		t.Errorf("expected verb=get, got %q", got[1].Verb)
	}
	if got[1].GroupResource.Group != "widgets.templates.krateo.io" {
		t.Errorf("expected group=widgets.templates.krateo.io, got %q", got[1].GroupResource.Group)
	}
	if got[1].GroupResource.Resource != "panels" {
		t.Errorf("expected resource=panels, got %q", got[1].GroupResource.Resource)
	}
	if got[1].Namespace != "krateo-system" {
		t.Errorf("expected namespace=krateo-system, got %q", got[1].Namespace)
	}
}

func TestBuildUserCanTargets_VerbAlwaysGet(t *testing.T) {
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "default"},
		{Group: "apps", Resource: "statefulsets", Namespace: "default"},
		{Group: "batch", Resource: "jobs", Namespace: "prod"},
	}

	got := buildUserCanTargets(targets)
	for i, uct := range got {
		if uct.Verb != "get" {
			t.Errorf("target[%d]: expected verb=get, got %q", i, uct.Verb)
		}
	}
}

func TestBuildUserCanTargets_VersionExcluded(t *testing.T) {
	// ResourceTarget doesn't carry version; verify the conversion
	// produces a GroupResource without version information.
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "default"},
	}
	got := buildUserCanTargets(targets)

	// schema.GroupResource has no Version field — this test documents the contract.
	expected := schema.GroupResource{Group: "apps", Resource: "deployments"}
	if got[0].GroupResource != expected {
		t.Errorf("expected GroupResource=%v, got %v", expected, got[0].GroupResource)
	}
}

// --- Unit tests for filterByDecisions ---

func TestFilterByDecisions_AllAllowed(t *testing.T) {
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "prod"},
		{Group: "apps", Resource: "deployments", Namespace: "staging"},
	}
	ucts := buildUserCanTargets(targets)

	decisions := map[plumbingRbac.UserCanTarget]bool{
		ucts[0]: true,
		ucts[1]: true,
	}

	got := filterByDecisions(targets, ucts, decisions)
	if len(got) != 2 {
		t.Fatalf("expected 2 allowed, got %d", len(got))
	}
}

func TestFilterByDecisions_AllDenied(t *testing.T) {
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "prod"},
		{Group: "apps", Resource: "deployments", Namespace: "staging"},
	}
	ucts := buildUserCanTargets(targets)

	decisions := map[plumbingRbac.UserCanTarget]bool{
		ucts[0]: false,
		ucts[1]: false,
	}

	got := filterByDecisions(targets, ucts, decisions)
	if len(got) != 0 {
		t.Fatalf("expected 0 allowed, got %d", len(got)) // this would be a leak of resources that should be denied
	}
}

func TestFilterByDecisions_Partial(t *testing.T) {
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "prod"},
		{Group: "apps", Resource: "deployments", Namespace: "staging"},
		{Group: "widgets.templates.krateo.io", Resource: "panels", Namespace: "krateo-system"},
	}
	ucts := buildUserCanTargets(targets)

	decisions := map[plumbingRbac.UserCanTarget]bool{
		ucts[0]: true,  // prod deployments: allowed
		ucts[1]: false, // staging deployments: denied
		ucts[2]: true,  // panels: allowed
	}

	got := filterByDecisions(targets, ucts, decisions)
	if len(got) != 2 {
		t.Fatalf("expected 2 allowed, got %d", len(got))
	}
	if got[0].Namespace != "prod" {
		t.Errorf("expected first allowed=prod, got %q", got[0].Namespace)
	}
	if got[1].Resource != "panels" {
		t.Errorf("expected second allowed=panels, got %q", got[1].Resource)
	}
}

func TestFilterByDecisions_MissingDecision(t *testing.T) {
	// If a target has no entry in decisions, it should be treated as denied.
	targets := []sql.ResourceTarget{
		{Group: "apps", Resource: "deployments", Namespace: "prod"},
	}
	ucts := buildUserCanTargets(targets)

	decisions := map[plumbingRbac.UserCanTarget]bool{} // empty map

	got := filterByDecisions(targets, ucts, decisions)
	if len(got) != 0 {
		t.Fatalf("expected 0 allowed when decision is missing, got %d", len(got))
	}
}

func TestFilterByDecisions_EmptyTargets(t *testing.T) {
	got := filterByDecisions(nil, nil, nil)
	if len(got) != 0 {
		t.Fatalf("expected empty result for nil inputs, got %d", len(got))
	}
}

// --- Unit test for FilterAllowed edge case ---

func TestRbacAuthorizer_FilterAllowed_EmptyTargets(t *testing.T) {
	auth := RbacAuthorizer{}

	// Empty targets should return nil without calling rbac.UserCan.
	got := auth.FilterAllowed(context.Background(), nil)
	if got != nil {
		t.Fatalf("expected nil for empty targets, got %v", got)
	}

	got = auth.FilterAllowed(context.Background(), []sql.ResourceTarget{})
	if got != nil {
		t.Fatalf("expected nil for zero-length targets, got %v", got)
	}
}
