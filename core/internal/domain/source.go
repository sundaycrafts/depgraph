package domain

import (
	"fmt"
	"io/fs"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// FileOp categorises a filesystem change observed under a project root.
type FileOp int

const (
	FileCreated FileOp = iota
	FileModified
	FileDeleted
)

// String returns the lowercase name of op (used in logs).
func (o FileOp) String() string {
	switch o {
	case FileCreated:
		return "create"
	case FileModified:
		return "modify"
	case FileDeleted:
		return "delete"
	}
	return "?"
}

// SourceChange describes a change detected under a project root. The Path
// is absolute on disk; subscribers translate to a file:// URI themselves
// because the URI scheme is LSP-specific.
type SourceChange struct {
	Path string
	Op   FileOp
}

// RelPathInRoot returns the path relative to root and reports whether the
// relative form stays within root (i.e. does not start with "..").
func RelPathInRoot(root, path string) (string, bool) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", false
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", false
	}
	return rel, true
}

// IsExcluded reports whether rel matches any of the given doublestar glob
// patterns. The first invalid pattern (if any) is returned alongside the
// match decision so the caller can surface configuration errors instead
// of silently dropping filters.
func IsExcluded(rel string, excludes []string) (bool, error) {
	var firstErr error
	for _, p := range excludes {
		ok, err := doublestar.PathMatch(p, rel)
		if err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("pattern %q: %w", p, err)
			}
			continue
		}
		if ok {
			return true, firstErr
		}
	}
	return false, firstErr
}

// MatchesAnyExt reports whether path ends in one of the supplied
// extensions. Extensions should include the leading dot (".go", ".ts").
func MatchesAnyExt(path string, exts []string) bool {
	for _, ext := range exts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// FileURIFromPath converts an absolute filesystem path to a file:// URI.
func FileURIFromPath(p string) string {
	u := &url.URL{Scheme: "file", Path: p}
	return u.String()
}

// PathFromFileURI is the inverse of FileURIFromPath. Returns the original
// string unmodified if it cannot be parsed.
func PathFromFileURI(uri string) string {
	u, err := url.Parse(uri)
	if err != nil {
		return uri
	}
	return u.Path
}

// WalkSourceFiles walks root collecting files whose extension matches any
// of exts. Dot entries (other than root itself) are always skipped. The
// excludes slice contains doublestar glob patterns matched against paths
// relative to root; matched files and directories are skipped.
//
// Invalid glob patterns are returned as the first error. Filesystem
// errors during the walk also propagate.
func WalkSourceFiles(root string, exts, excludes []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		for _, p := range excludes {
			ok, mErr := doublestar.PathMatch(p, rel)
			if mErr != nil {
				return fmt.Errorf("invalid exclude pattern %q: %w", p, mErr)
			}
			if ok {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
		}
		if d.IsDir() {
			return nil
		}
		if MatchesAnyExt(path, exts) {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// WalkDirectories walks root collecting every directory not skipped by
// the dot-prefix rule or the exclude patterns. The traversal mirrors
// WalkSourceFiles so filesystem watchers can install watches over the
// same set of directories that source-file lookups visit.
func WalkDirectories(root string, excludes []string) ([]string, error) {
	var dirs []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		if d.Name() != "." && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel != "." {
			for _, p := range excludes {
				ok, mErr := doublestar.PathMatch(p, rel)
				if mErr != nil {
					return fmt.Errorf("invalid exclude pattern %q: %w", p, mErr)
				}
				if ok {
					return filepath.SkipDir
				}
			}
		}
		dirs = append(dirs, path)
		return nil
	})
	return dirs, err
}
