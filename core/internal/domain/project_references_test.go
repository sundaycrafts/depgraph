package domain

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sundaycrafts/depgraph/internal/lsploader"
)

// fakeSession satisfies PortAnalysisSession with scripted responses. It
// is used to drive Project.FindReferences without spinning up a real LSP.
type fakeSession struct {
	lang       lsploader.Language
	references map[string][]ReferenceLocation // uri → returned refs
	docs       map[string][]DocumentSymbol    // uri → returned doc symbols
	calls      []string                       // observed References URIs (for assertion)
}

func (f *fakeSession) Lang() lsploader.Language { return f.lang }

func (f *fakeSession) DocumentSymbol(_ context.Context, uri string) ([]DocumentSymbol, error) {
	return f.docs[uri], nil
}

func (f *fakeSession) References(_ context.Context, uri string, _ Position) ([]ReferenceLocation, error) {
	f.calls = append(f.calls, uri)
	return f.references[uri], nil
}

func (f *fakeSession) Shutdown() {}

// TestFindReferences_SkipsNodeModules feeds the BFS a reference that lives
// inside node_modules and asserts that:
//
//   - the result list does not include the node_modules-residing symbol;
//   - the BFS never recurses by issuing a References call against the
//     node_modules URI (otherwise the loop would walk an arbitrarily deep
//     library graph).
//
// node_modules/ is part of TypeScript's DefaultExcludes, so the project
// should filter it without the user having to spell it out in
// add_project's excludes.
func TestFindReferences_SkipsNodeModules(t *testing.T) {
	root := "/proj"
	libURI := FileURIFromPath(filepath.Join(root, "node_modules", "zod", "lib", "z.d.ts"))
	userFile := filepath.Join(root, "src", "index.ts")
	userURI := FileURIFromPath(userFile)

	target := SymbolID{
		Lang:    lsploader.TypeScript,
		RelPath: "src/index.ts",
		Line:    2,
		Char:    14,
		Name:    "greetingSchema",
	}
	startURI := FileURIFromPath(filepath.Join(root, target.RelPath))

	fake := &fakeSession{
		lang: lsploader.TypeScript,
		references: map[string][]ReferenceLocation{
			// References on the user symbol return one user-side caller and
			// one node_modules hit. Only the user-side hit should survive
			// the exclude filter.
			startURI: {
				{URI: userURI, Range: Range{Start: Position{Line: 5, Character: 4}, End: Position{Line: 5, Character: 18}}},
				{URI: libURI, Range: Range{Start: Position{Line: 100, Character: 0}, End: Position{Line: 100, Character: 10}}},
			},
		},
		docs: map[string][]DocumentSymbol{
			userURI: {
				{
					Name:           "main",
					Range:          Range{Start: Position{Line: 4}, End: Position{Line: 8}},
					SelectionRange: Range{Start: Position{Line: 4, Character: 16}},
				},
			},
			libURI: {
				{
					Name:           "ZodInternal",
					Range:          Range{Start: Position{Line: 0}, End: Position{Line: 200}},
					SelectionRange: Range{Start: Position{Line: 0, Character: 0}},
				},
			},
		},
	}

	project := projectWithReadySession(t, root, lsploader.TypeScript, fake)

	got, err := project.FindReferences(context.Background(), target)
	if err != nil {
		t.Fatalf("FindReferences: %v", err)
	}

	if len(got.Results) != 1 {
		t.Fatalf("expected exactly one caller (main), got %d: %+v", len(got.Results), got.Results)
	}
	if got.Results[0].Name != "main" {
		t.Errorf("first result Name=%q, want %q", got.Results[0].Name, "main")
	}
	for _, s := range got.Results {
		if strings.Contains(s.Path, "node_modules") {
			t.Errorf("node_modules symbol leaked into results: %+v", s)
		}
	}

	// The BFS must never have queued the node_modules URI: the libURI
	// should not appear in the recorded call log. (The user URI may
	// appear more than once because the BFS walks each distinct caller
	// position, which for our fixture lives in the same file.)
	for _, called := range fake.calls {
		if called == libURI {
			t.Errorf("BFS issued a References call against node_modules: %v", fake.calls)
		}
	}
}

// projectWithReadySession constructs a Project whose single language
// session is in IndexReady, holding the supplied PortAnalysisSession.
// Used only by tests that exercise the BFS without real LSP startup.
func projectWithReadySession(t *testing.T, root string, lang lsploader.Language, sess PortAnalysisSession) *Project {
	t.Helper()
	p := &Project{
		Root:      root,
		Languages: []lsploader.Language{lang},
		logger:    slog.Default(),
		sessions:  make(map[lsploader.Language]*sessionEntry),
	}
	p.sessions[lang] = &sessionEntry{state: IndexReady, session: sess}
	return p
}
