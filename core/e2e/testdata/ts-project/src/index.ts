import { z } from 'zod';
import { greet } from './greeter';

// greetingSchema is the entry point's dependency on a zod symbol. The
// validator module also imports z.string(); both files share this same
// upstream symbol from node_modules/zod.
export const greetingSchema = z.string();

export function main(): void {
    const name = greetingSchema.parse('world');
    greet(name);
}
