import { useQuery } from '@tanstack/react-query'
import { FileContentSchema } from '../schemas/api'

export function useFile(path: string | undefined) {
  return useQuery({
    queryKey: ['file', path],
    enabled: !!path,
    queryFn: async () => {
      const res = await fetch(`/file?path=${encodeURIComponent(path!)}`)
      if (!res.ok) throw new Error(`File not found: ${res.status}`)
      return FileContentSchema.parse(await res.json())
    },
  })
}
