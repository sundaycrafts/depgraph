package domain

// PartialResult wraps the typed result list of a query with an optional
// list of human-readable warnings. Warnings are emitted when a downstream
// LSP call fails partway through aggregation or BFS — without them the
// caller has no signal that the result list is incomplete. The Warnings
// slice is omitted from JSON when empty so the happy path stays concise:
// {"results":[...]}.
type PartialResult[T any] struct {
	Results  []T      `json:"results"`
	Warnings []string `json:"warnings,omitempty"`
}

// AppendResult appends r to p.Results. Convenience to avoid &p.Results
// gymnastics in callers.
func (p *PartialResult[T]) AppendResult(r T) {
	p.Results = append(p.Results, r)
}

// AppendWarning appends w to p.Warnings.
func (p *PartialResult[T]) AppendWarning(w string) {
	p.Warnings = append(p.Warnings, w)
}
