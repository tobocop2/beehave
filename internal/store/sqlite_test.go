package store_test

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/store"
)

func TestSQLiteDSN_RunsTheBusyTimeoutBeforeThePragmasOfTheCaller(t *testing.T) {
	t.Parallel()

	address, err := url.Parse(store.SQLiteDSN("/a/b.db", 5, url.Values{"_pragma": {"journal_mode(WAL)"}}))

	require.NoError(t, err)
	assert.Equal(t, []string{"busy_timeout(5)", "journal_mode(WAL)"}, address.Query()["_pragma"])
}

func TestSQLiteDSN_AddsTheBusyTimeoutToTheCallersSettings(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "file:///a/b.db?_pragma=busy_timeout%285%29", store.SQLiteDSN("/a/b.db", 5, nil))
	assert.Equal(t, "file:///a/b.db?_pragma=busy_timeout%285%29&mode=ro", store.SQLiteDSN("/a/b.db", 5, url.Values{"mode": {"ro"}}))
}
