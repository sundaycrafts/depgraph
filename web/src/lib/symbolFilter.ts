import type { Graph } from '../schemas/api'

export type Language = 'go' | 'rust' | 'typescript' | 'default'

export function detectLanguage(graph: Graph): Language {
  for (const node of graph.nodes) {
    if (node.path?.endsWith('.go')) return 'go'
    if (node.path?.endsWith('.rs')) return 'rust'
    if (node.path?.endsWith('.ts') || node.path?.endsWith('.tsx')) return 'typescript'
  }
  return 'default'
}

// Per-language display overrides for symbolKind strings.
// e.g. in Rust context, LSP "interface" maps to "trait".
const KIND_LABEL_OVERRIDES: Partial<Record<Language, Record<string, string>>> = {
  rust: { interface: 'trait' },
}

export function getLabel(kind: string, lang: Language): string {
  return KIND_LABEL_OVERRIDES[lang]?.[kind] ?? kind
}

// Inverse of getLabel: label → symbolKind value
export function kindFromLabel(label: string, lang: Language): string {
  const overrides = KIND_LABEL_OVERRIDES[lang]
  if (!overrides) return label
  const entry = Object.entries(overrides).find(([, v]) => v === label)
  return entry ? entry[0] : label
}

// Default selected symbolKind values per language.
export const PRESETS: Record<Language, string[]> = {
  go:         ['function', 'interface', 'method'],
  typescript: ['function', 'interface', 'class', 'method', 'constant', 'variable'],
  rust:       ['function', 'interface', 'method'],  // "interface" = trait in rust-analyzer
  default:    [],                                   // no filter
}
