package ports

import (
	"context"

	"github.com/sundaycrafts/depgraph/internal/domain"
)

// ServerPort abstracts HTTP serving, static file delivery, and routing details.
type ServerPort interface {
	Serve(ctx context.Context, graph domain.Graph) error
}
