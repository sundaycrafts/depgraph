import type { Graph, Node } from '../schemas/api'

export const NODE_LIMIT = 2000

export function selectVisibleNodes(
  graph: Graph,
  selectedKinds: string[],
  limitNodes: boolean,
): Node[] {
  const filtered = graph.nodes.filter((n) => {
    if (n.kind === 'file') {
      return selectedKinds.includes('file')
    }
    return (
      selectedKinds.length === 0 ||
      (n.symbolKind != null && selectedKinds.includes(n.symbolKind))
    )
  })
  return limitNodes ? filtered.slice(0, NODE_LIMIT) : filtered
}
