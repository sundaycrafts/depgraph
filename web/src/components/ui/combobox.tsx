import * as React from 'react'
import * as Popover from '@radix-ui/react-popover'
import { Check, X, ChevronDown } from 'lucide-react'
import { cn } from '@/lib/utils'

// ---------------------------------------------------------------------------
// Context
// ---------------------------------------------------------------------------

interface ComboboxContextValue {
  items: string[]
  selectedValues: string[]
  setSelectedValues: (values: string[]) => void
  searchQuery: string
  setSearchQuery: (q: string) => void
  open: boolean
  setOpen: (open: boolean) => void
  multiple: boolean
}

const ComboboxContext = React.createContext<ComboboxContextValue | null>(null)

function useComboboxContext() {
  const ctx = React.useContext(ComboboxContext)
  if (!ctx) throw new Error('Combobox components must be used inside <Combobox>')
  return ctx
}

// ---------------------------------------------------------------------------
// useComboboxAnchor
// ---------------------------------------------------------------------------

export function useComboboxAnchor() {
  return React.useRef<HTMLDivElement>(null)
}

// ---------------------------------------------------------------------------
// Combobox (root)
// ---------------------------------------------------------------------------

interface ComboboxProps {
  multiple?: boolean
  autoHighlight?: boolean
  items: string[]
  defaultValue?: string[]
  value?: string[]
  onValueChange?: (values: string[]) => void
  children: React.ReactNode
}

export function Combobox({
  multiple = false,
  items,
  defaultValue,
  value: controlledValue,
  onValueChange,
  children,
}: ComboboxProps) {
  const [internalValues, setInternalValues] = React.useState<string[]>(defaultValue ?? [])
  const [searchQuery, setSearchQuery] = React.useState('')
  const [open, setOpen] = React.useState(false)

  const isControlled = controlledValue !== undefined
  const selectedValues = isControlled ? controlledValue : internalValues

  const setSelectedValues = React.useCallback((values: string[]) => {
    if (!isControlled) setInternalValues(values)
    onValueChange?.(values)
  }, [isControlled, onValueChange])

  return (
    <ComboboxContext.Provider value={{
      items, selectedValues, setSelectedValues,
      searchQuery, setSearchQuery,
      open, setOpen,
      multiple,
    }}>
      <Popover.Root open={open} onOpenChange={setOpen}>
        {children}
      </Popover.Root>
    </ComboboxContext.Provider>
  )
}

// ---------------------------------------------------------------------------
// ComboboxChips (trigger + chip container)
// ---------------------------------------------------------------------------

export const ComboboxChips = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, children, ...props }, ref) => {
  const { open } = useComboboxContext()
  return (
    <Popover.Trigger asChild>
      <div
        ref={ref}
        role="combobox"
        aria-expanded={open}
        className={cn(
          'flex flex-wrap gap-1 items-center min-h-8 px-2 py-1 rounded-md border border-input bg-background',
          'cursor-pointer text-sm focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring',
          className,
        )}
        {...props}
      >
        {children}
        <ChevronDown className="ml-auto h-3.5 w-3.5 shrink-0 text-muted-foreground opacity-50" />
      </div>
    </Popover.Trigger>
  )
})
ComboboxChips.displayName = 'ComboboxChips'

// ---------------------------------------------------------------------------
// ComboboxValue (render prop)
// ---------------------------------------------------------------------------

interface ComboboxValueProps {
  children: (values: string[]) => React.ReactNode
}

export function ComboboxValue({ children }: ComboboxValueProps) {
  const { selectedValues } = useComboboxContext()
  return <>{children(selectedValues)}</>
}

// ---------------------------------------------------------------------------
// ComboboxChip
// ---------------------------------------------------------------------------

interface ComboboxChipProps {
  children: React.ReactNode
  value?: string
}

export function ComboboxChip({ children, value }: ComboboxChipProps) {
  const { selectedValues, setSelectedValues } = useComboboxContext()

  function handleRemove(e: React.MouseEvent) {
    e.stopPropagation()
    if (value !== undefined) {
      setSelectedValues(selectedValues.filter(v => v !== value))
    }
  }

  return (
    <span className="inline-flex items-center gap-1 rounded bg-secondary px-1.5 py-0.5 text-xs font-medium text-secondary-foreground">
      {children}
      {value !== undefined && (
        <button
          type="button"
          onClick={handleRemove}
          className="rounded-full hover:bg-secondary-foreground/20 focus:outline-none"
          aria-label={`Remove ${children}`}
        >
          <X className="h-2.5 w-2.5" />
        </button>
      )}
    </span>
  )
}

// ---------------------------------------------------------------------------
// ComboboxChipsInput
// ---------------------------------------------------------------------------

export function ComboboxChipsInput({ className, ...props }: React.InputHTMLAttributes<HTMLInputElement>) {
  const { searchQuery, setSearchQuery, setOpen } = useComboboxContext()

  return (
    <input
      value={searchQuery}
      onChange={e => {
        setSearchQuery(e.target.value)
        setOpen(true)
      }}
      onClick={e => { e.stopPropagation(); setOpen(true) }}
      placeholder="Search..."
      className={cn(
        'flex-1 min-w-[80px] bg-transparent text-xs outline-none placeholder:text-muted-foreground',
        className,
      )}
      {...props}
    />
  )
}

// ---------------------------------------------------------------------------
// ComboboxContent
// ---------------------------------------------------------------------------

interface ComboboxContentProps {
  anchor?: React.RefObject<HTMLElement | null>
  children: React.ReactNode
  className?: string
}

export function ComboboxContent({ anchor: _anchor, children, className }: ComboboxContentProps) {
  const { setSearchQuery } = useComboboxContext()
  return (
    <Popover.Portal>
      <Popover.Content
        onCloseAutoFocus={() => setSearchQuery('')}
        align="start"
        sideOffset={4}
        className={cn(
          'z-50 min-w-[180px] rounded-md border bg-popover p-1 text-popover-foreground shadow-md',
          'data-[state=open]:animate-in data-[state=closed]:animate-out',
          'data-[state=closed]:fade-out-0 data-[state=open]:fade-in-0',
          'data-[state=closed]:zoom-out-95 data-[state=open]:zoom-in-95',
          className,
        )}
      >
        {children}
      </Popover.Content>
    </Popover.Portal>
  )
}

// ---------------------------------------------------------------------------
// ComboboxEmpty
// ---------------------------------------------------------------------------

export function ComboboxEmpty({ children }: { children: React.ReactNode }) {
  const { items, searchQuery } = useComboboxContext()
  const hasMatch = items.some(item =>
    item.toLowerCase().includes(searchQuery.toLowerCase())
  )
  if (hasMatch) return null
  return (
    <div className="py-2 px-2 text-xs text-muted-foreground text-center">
      {children}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ComboboxList
// ---------------------------------------------------------------------------

interface ComboboxListProps {
  children: (item: string) => React.ReactNode
  className?: string
}

export function ComboboxList({ children, className }: ComboboxListProps) {
  const { items, searchQuery } = useComboboxContext()
  const filtered = searchQuery
    ? items.filter(item => item.toLowerCase().includes(searchQuery.toLowerCase()))
    : items

  return (
    <div role="listbox" className={cn('max-h-48 overflow-y-auto', className)}>
      {filtered.map(item => (
        <React.Fragment key={item}>{children(item)}</React.Fragment>
      ))}
    </div>
  )
}

// ---------------------------------------------------------------------------
// ComboboxItem
// ---------------------------------------------------------------------------

interface ComboboxItemProps {
  value: string
  children: React.ReactNode
  className?: string
}

export function ComboboxItem({ value, children, className }: ComboboxItemProps) {
  const { selectedValues, setSelectedValues, multiple, setOpen, setSearchQuery } = useComboboxContext()
  const isSelected = selectedValues.includes(value)

  function handleSelect() {
    if (multiple) {
      setSelectedValues(
        isSelected
          ? selectedValues.filter(v => v !== value)
          : [...selectedValues, value],
      )
    } else {
      setSelectedValues([value])
      setOpen(false)
      setSearchQuery('')
    }
  }

  return (
    <div
      role="option"
      aria-selected={isSelected}
      onClick={handleSelect}
      className={cn(
        'flex items-center gap-2 rounded-sm px-2 py-1.5 text-xs cursor-pointer',
        'hover:bg-accent hover:text-accent-foreground',
        isSelected && 'font-medium',
        className,
      )}
    >
      <span className="w-3 shrink-0">
        {isSelected && <Check className="h-3 w-3" />}
      </span>
      {children}
    </div>
  )
}
