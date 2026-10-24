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

func TestStoredLease(t *testing.T) {
	t.Parallel()

	tests := map[string]int64{
		"": model.LeaseNone, "no lease here": model.LeaseNone, "lease=7200": 7200, "x lease=60 y": 60, "lease=60\nmore": 60,
		"lease=": model.LeaseUnreadable, "lease=0": model.LeaseUnreadable, "lease=abc": model.LeaseUnreadable, "lease=-5": model.LeaseUnreadable,
		"lease=1234567890123": model.LeaseUnreadable, "lease=9999999999999999999": model.LeaseUnreadable, "lease=1.5": model.LeaseUnreadable,
		"xlease=60": model.LeaseNone, "_lease=60": model.LeaseNone, "xlease=60 lease=30": 30, "(lease=45)": model.LeaseUnreadable,
	}
	for text, want := range tests {
		assert.Equal(t, want, model.StoredLease(text), text)
	}
}

func TestLeaseText_RoundTripsThroughStoredLease(t *testing.T) {
	t.Parallel()

	assert.Equal(t, "lease=7200", model.LeaseText(7200))
	assert.Equal(t, int64(7200), model.StoredLease("Claim for 5. "+model.LeaseText(7200)))
}
