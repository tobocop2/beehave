package model_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/beehave/internal/model"
)

func TestNotAuthorError_TheTextIsTheReason(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "its author is \"dev\", not the owner", (&model.NotAuthorError{Reason: "its author is \"dev\", not the owner"}).Error())
}

func TestReviewThread_Summary(t *testing.T) {
	t.Parallel()

	path, line, fix, x, long := "a.go", 4, "fix", "x", strings.Repeat("é", 81)

	tests := []struct {
		name   string
		thread model.ReviewThread
		want   string
	}{
		{"all_parts", model.ReviewThread{Path: &path, Line: &line, First: &model.ThreadComment{Author: model.Account{Login: "gz"}, Body: &fix}}, "a.go:4 gz: fix"},
		{"no_parts", model.ReviewThread{}, "null:null null: null"},
		{"null_body", model.ReviewThread{First: &model.ThreadComment{}}, "null:null null: null"},
		{"no_author", model.ReviewThread{First: &model.ThreadComment{Body: &x}}, "null:null null: x"},
		{"long_comment", model.ReviewThread{First: &model.ThreadComment{Body: &long}}, "null:null null: " + strings.Repeat("é", 80)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, tt.thread.Summary())
		})
	}
}

func TestChangeRequest_ShowsNineCharactersOfTheCommit(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "u@abc", model.ChangeRequest{Login: "u", Commit: "abc"}.String())
	assert.Equal(t, "u@012345678", model.ChangeRequest{Login: "u", Commit: "0123456789ab"}.String())
	assert.Equal(t, "u@012345678", model.ChangeRequest{Login: "u", Commit: "012345678"}.String())
	assert.Equal(t, "a@1,b@2", model.JoinChangeRequests([]model.ChangeRequest{{Login: "a", Commit: "1"}, {Login: "b", Commit: "2"}}))
	assert.Empty(t, model.JoinChangeRequests(nil))
}

func TestOrNull(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "null", model.OrNull(""))
	assert.Equal(t, "x", model.OrNull("x"))
}

func TestIsRepoPart(t *testing.T) {
	t.Parallel()

	for text, want := range map[string]bool{"": false, ".": false, "..": false, "...": true, ".a": true, "a-b_c.1": true, "A9": true, "a b": false, "a/b": false, "é": false, "a;": false} {
		assert.Equal(t, want, model.IsRepoPart(text), text)
	}
}
