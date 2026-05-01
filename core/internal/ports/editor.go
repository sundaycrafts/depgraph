package ports

// EditorPort abstracts file access (local FS, remote VFS, IDE virtual FS, etc.).
type EditorPort interface {
	GetFileContent(path string) (string, error)
}

// EditorFunc is a function adapter for EditorPort.
type EditorFunc func(path string) (string, error)

func (f EditorFunc) GetFileContent(path string) (string, error) { return f(path) }
