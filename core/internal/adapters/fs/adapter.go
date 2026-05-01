package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sundaycrafts/depgraph/internal/ports"
)

// Adapter implements ports.EditorPort via the local filesystem.
// Access is restricted to paths within the analyzed root to prevent path traversal.
type Adapter struct {
	root string
}

var _ ports.EditorPort = (*Adapter)(nil)

func New(root string) *Adapter {
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	return &Adapter{root: abs}
}

func (a *Adapter) GetFileContent(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("invalid path %q: %w", path, err)
	}
	rel, err := filepath.Rel(a.root, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q is outside root %q", path, a.root)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
