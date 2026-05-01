package fs

import (
	"github.com/sundaycrafts/depgraph/internal/ports"
)

// Adapter implements ports.EditorPort via the local filesystem.
// Access is restricted to paths within the analyzed root to prevent path traversal.
type Adapter struct {
	root string
}

var _ ports.EditorPort = (*Adapter)(nil)

func New(root string) *Adapter {
	return &Adapter{root: root}
}

func (a *Adapter) GetFileContent(_ string) (string, error) {
	panic("not implemented")
}
