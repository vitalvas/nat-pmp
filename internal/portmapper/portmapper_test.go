package portmapper

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSentinelErrors(t *testing.T) {
	all := []error{ErrUnsupported, ErrConflict, ErrNoResources, ErrNotAuthorized}

	t.Run("non-nil", func(t *testing.T) {
		for _, err := range all {
			require.Error(t, err)
		}
	})

	t.Run("distinct", func(t *testing.T) {
		for i := range all {
			for j := range all {
				if i == j {
					continue
				}
				assert.False(t, errors.Is(all[i], all[j]), "errors %d and %d must be distinct", i, j)
			}
		}
	})
}
