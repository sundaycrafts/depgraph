package domain

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

func TestProjectState_InitiallyIndexing(t *testing.T) {
	p := newTestProject(t, lsploader.Go)
	state, _ := p.State()
	if state != IndexIndexing {
		t.Errorf("state=%s want indexing", state)
	}
}

func TestProjectState_AllReady(t *testing.T) {
	p := newTestProject(t, lsploader.Go, lsploader.TypeScript)
	for lang := range p.sessions {
		p.sessions[lang].state = IndexReady
	}
	state, _ := p.State()
	if state != IndexReady {
		t.Errorf("state=%s want ready", state)
	}
}

func TestProjectState_OneFailedFailsAggregate(t *testing.T) {
	p := newTestProject(t, lsploader.Go, lsploader.TypeScript)
	p.sessions[lsploader.Go].state = IndexReady
	p.sessions[lsploader.TypeScript].state = IndexFailed
	p.sessions[lsploader.TypeScript].err = fmt.Errorf("boom")
	state, err := p.State()
	if state != IndexFailed {
		t.Errorf("state=%s want failed", state)
	}
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err=%v want to contain 'boom'", err)
	}
}

// newTestProject constructs a Project with the given languages and an
// empty session entry per language. No real watcher or session is
// attached — callers populate state directly.
func newTestProject(t *testing.T, langs ...lsploader.Language) *Project {
	t.Helper()
	p := &Project{
		Root:      "/tmp/test",
		Languages: append([]lsploader.Language(nil), langs...),
		logger:    slog.Default(),
		sessions:  make(map[lsploader.Language]*sessionEntry),
	}
	for _, l := range langs {
		p.sessions[l] = &sessionEntry{state: IndexIndexing}
	}
	return p
}
