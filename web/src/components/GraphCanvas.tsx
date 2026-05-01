import type { components } from '../gen/api'

type Graph = components['schemas']['Graph']

interface Props {
  graph: Graph
}

export function GraphCanvas({ graph }: Props) {
  return <div>GraphCanvas (stub) — {graph.nodes.length} nodes</div>
}
