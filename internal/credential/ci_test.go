package credential_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/tobocop2/beehave/internal/credential"
	"github.com/tobocop2/beehave/internal/model"
)

func TestCIStart_YesRefusesWhateverCILockedSaysAndUnknownStartsOnlyWithIt(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		answer model.CIWrite
		locked bool
		want   error
	}{
		"yes, not stated":     {model.CIWriteYes, false, credential.ErrCIWritable},
		"yes, stated":         {model.CIWriteYes, true, credential.ErrCIWritable},
		"no, not stated":      {model.CIWriteNo, false, nil},
		"no, stated":          {model.CIWriteNo, true, nil},
		"unknown, not stated": {model.CIWriteUnknown, false, credential.ErrCIUnstated},
		"unknown, stated":     {model.CIWriteUnknown, true, nil},
	} {
		require.ErrorIs(t, credential.CIStart(test.answer, test.locked), test.want, name)
	}

	require.ErrorContains(t, credential.CIStart(model.CIWriteYes, true), "ci_locked does not lift this")
	require.ErrorContains(t, credential.CIStart(model.CIWriteUnknown, false), "set ci_locked = True")
}
