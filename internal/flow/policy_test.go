package flow

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLogger_RemovesSecretsFromMessagesAttributesErrorsAndGroups(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	log := newLogger(&out).With("scope", "sk-ant-abcdefgh01").WithGroup("g")
	log.Info("token sk-ant-abcdefgh02", "text", "sk-ant-abcdefgh03", "err", errors.New("sk-ant-abcdefgh04"),
		"nested", slog.Group("inner", slog.String("k", "sk-ant-abcdefgh05")), "count", 7)
	log.Debug("below the level sk-ant-abcdefgh06")

	text := out.String()
	assert.NotContains(t, text, "sk-ant-")
	assert.Equal(t, 5, strings.Count(text, "[redacted]"))
	assert.Contains(t, text, "count=7")
	assert.NotContains(t, text, "below the level")
}

func TestLogger_EnabledFollowsTheInnerLevel(t *testing.T) {
	t.Parallel()

	handler := newLogger(&bytes.Buffer{}).Handler()

	assert.False(t, handler.Enabled(context.Background(), slog.LevelDebug))
	assert.True(t, handler.Enabled(context.Background(), slog.LevelInfo))
}
