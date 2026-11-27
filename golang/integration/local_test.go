//go:build integration

package integration

import (
	"path/filepath"
	"testing"
)

// TestLocal runs the scenario on a storage in a local directory.
func TestLocal(t *testing.T) {
	runScenario(t, WithStorageURL(filepath.Join(t.TempDir(), "storage")))
}
