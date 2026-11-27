//go:build !darwin

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDeveloperFolder_IsEmptyOnASystemWithNoDeveloperFolderOfApple(t *testing.T) {
	t.Parallel()

	assert.Empty(t, developerFolder([]string{"DEVELOPER_DIR=/d"}))
}
