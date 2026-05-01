package ports

// EditorPort abstracts file access (local FS, remote VFS, IDE virtual FS, etc.).
type EditorPort interface {
	GetFileContent(path string) (string, error)
}
