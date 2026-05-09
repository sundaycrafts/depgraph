import { z } from 'zod';

// validate uses the same zod symbol (z.string) that index.ts depends on.
// This fixture exists to verify depgraph does not recurse into
// node_modules when tracking the dependency chain — z.string is defined
// outside the project root and must not appear in the resulting graph.
export function validate(input: unknown): string {
    return z.string().parse(input);
}
