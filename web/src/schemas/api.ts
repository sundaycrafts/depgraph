import { z } from 'zod'

export const PositionSchema = z.object({
  line: z.number().int().min(0),
  character: z.number().int().min(0),
})

export const RangeSchema = z.object({
  start: PositionSchema,
  end: PositionSchema,
})

export const NodeSchema = z.object({
  id: z.string(),
  kind: z.enum(['file', 'symbol']),
  label: z.string(),
  path: z.string().optional(),
  symbolKind: z.string().optional(),
  range: RangeSchema.optional(),
})

export const EdgeSchema = z.object({
  id: z.string(),
  from: z.string(),
  to: z.string(),
  kind: z.enum(['defines', 'references']),
  confidence: z.enum(['exact', 'probable']),
})

export const GraphSchema = z.object({
  nodes: z.array(NodeSchema),
  edges: z.array(EdgeSchema),
})

export const FileContentSchema = z.object({
  path: z.string(),
  content: z.string(),
})

export type Graph = z.infer<typeof GraphSchema>
export type Node = z.infer<typeof NodeSchema>
export type Edge = z.infer<typeof EdgeSchema>
export type FileContent = z.infer<typeof FileContentSchema>
export type Range = z.infer<typeof RangeSchema>
