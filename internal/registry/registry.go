package registry

import (
	_ "embed"
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed resources.yaml
var embeddedResources []byte

// ResourceDef describes a supported Kubernetes resource type.
type ResourceDef struct {
	ShortName  string `yaml:"shortName"`
	APIVersion string `yaml:"apiVersion"`
	Kind       string `yaml:"kind"`
	// DBKind is the compound key used in the database (apiVersion:Kind).
	// Computed at load time, not stored in YAML.
	DBKind string `yaml:"-"`
}

// Registry holds the set of resource types that this service will serve.
type Registry struct {
	byShortName map[string]ResourceDef
	byDBKind    map[string]ResourceDef
}

type registryFile struct {
	Resources []ResourceDef `yaml:"resources"`
}

// Load parses the embedded resources.yaml that was baked in at compile time.
func Load() (*Registry, error) {
	return parse(embeddedResources)
}

// NewFromYAML parses a YAML byte slice into a Registry.
// Useful for tests or loading from an external source.
func NewFromYAML(data []byte) (*Registry, error) {
	return parse(data)
}

// parse is the internal constructor.
func parse(data []byte) (*Registry, error) {
	var f registryFile
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("registry: invalid YAML: %w", err)
	}

	r := &Registry{
		byShortName: make(map[string]ResourceDef, len(f.Resources)),
		byDBKind:    make(map[string]ResourceDef, len(f.Resources)),
	}

	for i, def := range f.Resources {
		if def.ShortName == "" || def.APIVersion == "" || def.Kind == "" {
			return nil, fmt.Errorf("registry: entry %d missing required fields", i)
		}
		def.DBKind = def.APIVersion + ":" + def.Kind

		if _, dup := r.byShortName[def.ShortName]; dup {
			return nil, fmt.Errorf("registry: duplicate shortName %q", def.ShortName)
		}
		if _, dup := r.byDBKind[def.DBKind]; dup {
			return nil, fmt.Errorf("registry: duplicate apiVersion:Kind %q", def.DBKind)
		}

		r.byShortName[def.ShortName] = def
		r.byDBKind[def.DBKind] = def
	}

	return r, nil
}

// Resolve looks up a resource by short name first, then by full apiVersion:Kind.
// Returns the definition and true if found, or zero value and false otherwise.
func (r *Registry) Resolve(name string) (ResourceDef, bool) {
	// Try short name first.
	if def, ok := r.byShortName[name]; ok {
		return def, true
	}
	// Fallback: check if it's a known full apiVersion:Kind.
	if def, ok := r.byDBKind[name]; ok {
		return def, true
	}
	return ResourceDef{}, false
}

// ShortNames returns all registered short names (useful for error messages).
func (r *Registry) ShortNames() []string {
	names := make([]string, 0, len(r.byShortName))
	for n := range r.byShortName {
		names = append(names, n)
	}
	return names
}

// String returns a human-readable summary of all registered resources.
func (r *Registry) String() string {
	var b strings.Builder
	for _, def := range r.byShortName {
		fmt.Fprintf(&b, "  %s -> %s\n", def.ShortName, def.DBKind)
	}
	return b.String()
}
