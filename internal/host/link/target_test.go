package link_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tobocop2/obee/internal/host/link"
	"github.com/tobocop2/obee/internal/model"
	"github.com/tobocop2/obee/internal/proc/proctest"
)

func TestTarget_IsTheNameTheLinkConnectsTo(t *testing.T) {
	t.Parallel()

	l, err := link.ForMachine("gates", link.Options{}, proctest.Always(model.CommandResult{}))

	require.NoError(t, err)
	assert.Equal(t, "gates", l.Target())
	assert.Equal(t, "head", link.New("head", proctest.Always(model.CommandResult{})).Target())
}
