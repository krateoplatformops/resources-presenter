package config

import "net/url"

func parseDBParams(raw string) (map[string]string, error) {
	if raw == "" {
		return map[string]string{}, nil
	}

	values, err := url.ParseQuery(raw)
	if err != nil {
		return nil, err
	}

	params := make(map[string]string, len(values))
	for k, v := range values {
		if len(v) > 0 {
			params[k] = v[0] // PostgreSQL does not support multiple values
		}
	}

	return params, nil
}
