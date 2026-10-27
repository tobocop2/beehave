package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/tobocop2/obee/internal/model"
)

func TestItemKind(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"2021": "gh", "0": "gh", "proj-a1b.2": "bd", "a-b": "bd", "A_1-x_y.z-9": "bd",
		"": "", "abc": "", "-x": "", "a-": "", "12a": "", "a b-c": "", "a-b c": "", "a/b-c": "", "٣": "", "_a-b": "", "a.b-c": "", "é-b": "",
	}
	for item, want := range tests {
		assert.Equal(t, want, model.ItemKind(item), item)
	}
}
