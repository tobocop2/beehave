package store

import (
	"fmt"
	"net/url"
)

// SQLiteDSN returns the address of a SQLite file for the modernc driver. Each connection waits busyMs milliseconds for a lock; params adds its own settings.
// The driver runs the pragmas in order, so busy_timeout comes first: a later pragma that needs a lock then waits for it.
func SQLiteDSN(path string, busyMs int, params url.Values) string {
	query := url.Values{"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyMs)}}

	for key, values := range params {
		for _, value := range values {
			query.Add(key, value)
		}
	}

	return (&url.URL{Scheme: "file", Path: path, RawQuery: query.Encode()}).String()
}
