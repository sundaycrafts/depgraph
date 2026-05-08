package domain

// PortEditor abstracts file-content access (local FS, remote VFS, IDE
// virtual FS, etc.).
type PortEditor interface {
	GetFileContent(path string) (string, error)
}

// PortEditorFunc is a function adapter for PortEditor.
type PortEditorFunc func(path string) (string, error)

// GetFileContent satisfies PortEditor.
func (f PortEditorFunc) GetFileContent(path string) (string, error) { return f(path) }
