import { useQuery } from '@tanstack/react-query'
import { GraphSchema } from '../schemas/api'

export function useGraph() {
  return useQuery({
    queryKey: ['graph'],
    queryFn: async () => {
      const res = await fetch('/graph')
      if (!res.ok) throw new Error(`Failed to fetch graph: ${res.status}`)
      return GraphSchema.parse(await res.json())
    },
  })
}
