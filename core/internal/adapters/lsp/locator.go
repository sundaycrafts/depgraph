package lsp

import "os/exec"

// ExecLocator implements lsploader.Locator using exec.LookPath.
// It is the production implementation — it touches the real PATH.
type ExecLocator struct{}

func (ExecLocator) LookupBinary(name string) (string, error) {
	return exec.LookPath(name)
}
