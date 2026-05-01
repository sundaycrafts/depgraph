package ports

import "context"

// ServerPort abstracts HTTP serving, static file delivery, and routing details.
type ServerPort interface {
	Serve(ctx context.Context) error
}
