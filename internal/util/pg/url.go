package pg

import (
	"net/url"
	"strconv"
)

// ConnectionURL builds a PostgreSQL connection url.
func ConnectionURL(
	username string,
	password string,
	host string,
	port int,
	dbname string,
	extraParams map[string]string,
) (string, error) {

	u := &url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(username, password),
		Host:   host + ":" + strconv.Itoa(port),
		Path:   "/" + dbname,
	}

	if len(extraParams) > 0 {
		q := url.Values{}
		for k, v := range extraParams {
			q.Set(k, v)
		}
		u.RawQuery = q.Encode()
	}

	return u.String(), nil
}
