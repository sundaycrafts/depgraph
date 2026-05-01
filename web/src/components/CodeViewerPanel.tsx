import type { components } from '../gen/api'

type Node = components['schemas']['Node']

interface Props {
  node: Node | null
}

export function CodeViewerPanel({ node }: Props) {
  if (!node) return null
  return <div>CodeViewerPanel (stub) — {node.label}</div>
}
