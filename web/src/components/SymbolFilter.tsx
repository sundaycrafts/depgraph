import * as React from 'react'
import {
  Combobox,
  ComboboxChip,
  ComboboxChips,
  ComboboxChipsInput,
  ComboboxContent,
  ComboboxEmpty,
  ComboboxItem,
  ComboboxList,
  ComboboxValue,
  useComboboxAnchor,
} from '@/components/ui/combobox'
import { detectLanguage, getLabel, kindFromLabel, PRESETS } from '@/lib/symbolFilter'
import type { Graph } from '../schemas/api'

interface Props {
  graph: Graph
  selectedKinds: string[]
  onKindsChange: (kinds: string[]) => void
}

export function SymbolFilter({ graph, selectedKinds, onKindsChange }: Props) {
  const anchor = useComboboxAnchor()
  const lang = detectLanguage(graph)

  // Collect unique symbolKinds from symbol nodes; always prepend "file".
  const availableKinds = React.useMemo(() => {
    const seen = new Set<string>()
    for (const node of graph.nodes) {
      if (node.kind === 'symbol' && node.symbolKind) seen.add(node.symbolKind)
    }
    return ['file', ...Array.from(seen).sort()]
  }, [graph])

  // Apply language preset once when availableKinds are first populated.
  const presetApplied = React.useRef(false)
  React.useEffect(() => {
    if (availableKinds.length === 0 || presetApplied.current) return
    presetApplied.current = true
    const preset = PRESETS[lang].filter(k => availableKinds.includes(k))
    onKindsChange(preset)
  }, [availableKinds, lang, onKindsChange])

  // Combobox works with display labels; translate to/from symbolKind values.
  const items = availableKinds.map(k => getLabel(k, lang))
  const selectedLabels = selectedKinds.map(k => getLabel(k, lang))

  function handleValueChange(labels: string[]) {
    onKindsChange(labels.map(l => kindFromLabel(l, lang)))
  }

  return (
    <Combobox
      multiple
      autoHighlight
      items={items}
      value={selectedLabels}
      onValueChange={handleValueChange}
    >
      <ComboboxChips ref={anchor} className="min-w-[200px] max-w-sm">
        <ComboboxValue>
          {(values) => (
            <React.Fragment>
              {values.map((value) => (
                <ComboboxChip key={value} value={value}>{value}</ComboboxChip>
              ))}
              <ComboboxChipsInput />
            </React.Fragment>
          )}
        </ComboboxValue>
      </ComboboxChips>
      <ComboboxContent anchor={anchor}>
        <ComboboxEmpty>No symbol kinds found.</ComboboxEmpty>
        <ComboboxList>
          {(item) => (
            <ComboboxItem key={item} value={item}>
              {item}
            </ComboboxItem>
          )}
        </ComboboxList>
      </ComboboxContent>
    </Combobox>
  )
}
