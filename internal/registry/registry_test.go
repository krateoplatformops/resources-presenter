package registry

import (
	"testing"
)

func TestLoad(t *testing.T) {
	r, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	// The embedded resources.yaml must contain at least "panels".
	def, ok := r.Resolve("panels")
	if !ok {
		t.Fatal("expected 'panels' to be registered")
	}
	if def.DBKind != "widgets.templates.krateo.io/v1beta1:Panel" {
		t.Fatalf("unexpected DBKind: %s", def.DBKind)
	}
}

func TestResolveShortName(t *testing.T) {
	r := mustParse(t, `
resources:
  - shortName: deployments
    apiVersion: apps/v1
    kind: Deployment
`)
	def, ok := r.Resolve("deployments")
	if !ok {
		t.Fatal("expected 'deployments' to resolve")
	}
	if def.DBKind != "apps/v1:Deployment" {
		t.Fatalf("unexpected DBKind: %s", def.DBKind)
	}
}

func TestResolveFullFormat(t *testing.T) {
	r := mustParse(t, `
resources:
  - shortName: deployments
    apiVersion: apps/v1
    kind: Deployment
`)
	def, ok := r.Resolve("apps/v1:Deployment")
	if !ok {
		t.Fatal("expected full format to resolve")
	}
	if def.ShortName != "deployments" {
		t.Fatalf("unexpected ShortName: %s", def.ShortName)
	}
}

func TestResolveUnknown(t *testing.T) {
	r := mustParse(t, `
resources:
  - shortName: pods
    apiVersion: v1
    kind: Pod
`)
	_, ok := r.Resolve("unknown")
	if ok {
		t.Fatal("expected unknown resource to not resolve")
	}
}

func TestDuplicateShortName(t *testing.T) {
	_, err := parse([]byte(`
resources:
  - shortName: pods
    apiVersion: v1
    kind: Pod
  - shortName: pods
    apiVersion: v1
    kind: Service
`))
	if err == nil {
		t.Fatal("expected error for duplicate shortName")
	}
}

func TestDuplicateDBKind(t *testing.T) {
	_, err := parse([]byte(`
resources:
  - shortName: pods
    apiVersion: v1
    kind: Pod
  - shortName: corePods
    apiVersion: v1
    kind: Pod
`))
	if err == nil {
		t.Fatal("expected error for duplicate apiVersion:Kind")
	}
}

func TestMissingFields(t *testing.T) {
	_, err := parse([]byte(`
resources:
  - shortName: pods
    apiVersion: v1
`))
	if err == nil {
		t.Fatal("expected error for missing kind")
	}
}

func TestShortNames(t *testing.T) {
	r := mustParse(t, `
resources:
  - shortName: pods
    apiVersion: v1
    kind: Pod
  - shortName: deployments
    apiVersion: apps/v1
    kind: Deployment
`)
	names := r.ShortNames()
	if len(names) != 2 {
		t.Fatalf("expected 2 short names, got %d", len(names))
	}
}

func mustParse(t *testing.T, yaml string) *Registry {
	t.Helper()
	r, err := parse([]byte(yaml))
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	return r
}
