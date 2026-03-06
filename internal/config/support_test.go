package config

import (
	"reflect"
	"testing"
)

func TestParseDBParams(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    map[string]string
		wantErr bool
	}{
		{
			name: "empty input",
			raw:  "",
			want: map[string]string{},
		},
		{
			name: "single param",
			raw:  "sslmode=disable",
			want: map[string]string{"sslmode": "disable"},
		},
		{
			name: "multiple params",
			raw:  "sslmode=disable&keepalives=1&keepalives_idle=30&keepalives_interval=10&keepalives_count=3",
			want: map[string]string{
				"sslmode":             "disable",
				"keepalives":          "1",
				"keepalives_idle":     "30",
				"keepalives_interval": "10",
				"keepalives_count":    "3",
			},
		},
		{
			name: "same key repeated keeps first",
			raw:  "sslmode=disable&sslmode=require",
			want: map[string]string{"sslmode": "disable"},
		},
		{
			name:    "invalid escape sequence",
			raw:     "sslmode=%zz",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseDBParams(tt.raw)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("parseDBParams(%q) expected error, got nil", tt.raw)
				}
				return
			}

			if err != nil {
				t.Fatalf("parseDBParams(%q) error = %v", tt.raw, err)
			}

			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseDBParams(%q) = %#v, want %#v", tt.raw, got, tt.want)
			}
		})
	}
}