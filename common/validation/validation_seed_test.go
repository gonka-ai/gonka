package validation

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidationReplaySeedUsesNonNegativeInt32Range(t *testing.T) {
	assert.Equal(t, int32(0), validationReplaySeed("inf-1"))
	assert.Equal(t, int32(42), validationReplaySeed("42"))
	assert.Equal(t, int32(math.MaxInt32), validationReplaySeed("2147483647"))
	assert.Equal(t, int32(math.MaxInt32), validationReplaySeed("2147483648"))
	assert.Equal(t, int32(math.MaxInt32), validationReplaySeed("4294967295"))
}
