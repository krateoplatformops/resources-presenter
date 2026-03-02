package pg

import (
	"net/url"
	"strings"
	"testing"
)

func TestConnectionURL(t *testing.T) {
	tests := []struct {
		name   string
		params map[string]string
	}{
		{
			name: "without extra params",
		},
		{
			name: "with multiple extra params",
			params: map[string]string{
				"sslmode":             "disable",
				"keepalives":          "1",
				"keepalives_idle":     "30",
				"keepalives_interval": "10",
				"keepalives_count":    "3",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dsn, err := ConnectionURL(
				"test",
				"test",
				"postgres.demo-system.svc.cluster.local",
				5432,
				"testdb",
				tt.params,
			)
			if err != nil {
				t.Fatalf("ConnectionURL() error = %v", err)
			}

			u, err := url.Parse(dsn)
			if err != nil {
				t.Fatalf("url.Parse(%q) error = %v", dsn, err)
			}

			if got := u.Scheme; got != "postgres" {
				t.Fatalf("scheme = %q, want %q", got, "postgres")
			}

			if got := u.Host; got != "postgres.demo-system.svc.cluster.local:5432" {
				t.Fatalf("host = %q, want %q", got, "postgres.demo-system.svc.cluster.local:5432")
			}

			if got := u.Path; got != "/testdb" {
				t.Fatalf("path = %q, want %q", got, "/testdb")
			}

			q := u.Query()
			gotParams := make(map[string]string, len(q))
			for k, v := range q {
				if len(v) > 0 {
					gotParams[k] = v[0]
				}
			}

			if len(gotParams) != len(tt.params) {
				t.Fatalf("query params len = %d, want %d (%#v)", len(gotParams), len(tt.params), gotParams)
			}

			for k, v := range tt.params {
				if gotParams[k] != v {
					t.Fatalf("query param %q = %q, want %q", k, gotParams[k], v)
				}
			}

			if strings.Contains(dsn, `\\u0026`) {
				t.Fatalf("dsn contains JSON-escaped ampersand: %q", dsn)
			}
		})
	}
}