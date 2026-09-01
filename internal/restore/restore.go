package restore

import "github.com/joecattt/thaw/pkg/models"

// Target is the interface for workspace restore backends.
type Target interface {
	Name() string
	Available() bool
	Restore(snap *models.Snapshot, opts models.RestoreOptions) error
	GenerateScript(snap *models.Snapshot, opts models.RestoreOptions) (string, error)
}
