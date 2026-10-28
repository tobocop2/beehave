package claim

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestAge(t *testing.T) {
	t.Parallel()

	tests := map[time.Duration]string{0: "0h00m", 59 * time.Second: "0h00m", time.Minute: "0h01m", 3599 * time.Second: "0h59m", time.Hour: "1h00m", 7260 * time.Second: "2h01m"}
	for span, want := range tests {
		assert.Equal(t, want, age(span))
	}
}
