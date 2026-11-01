/**
 * Standard studio index page: header + toolbar (search, New button) +
 * hairline table with hover rows, skeleton loading rows and an empty state.
 * Filtering is client-side, matching `name`/`slug` string fields.
 */
import { useState, type ReactNode } from 'react'
import { Inbox, Plus, Search } from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { cx } from '../ui/cx'
import { Button } from '../ui/Button'
import { Input } from '../ui/Form'
import { EmptyState, Skeleton } from '../ui/Display'
import { PageHeader } from './PageHeader'

export interface ListPageColumn<R> {
  key: string
  header: string
  /** Width class applied to the header + cells, e.g. `w-40`. */
  width?: string
  /** Cell content; defaults to the row's `key` field when it is a string/number. */
  render?: (row: R) => ReactNode
}

export interface ListPageProps<R> {
  title: ReactNode
  description?: ReactNode
  newButton: { label: string; onClick: () => void }
  searchPlaceholder?: string
  columns: ListPageColumn<R>[]
  /** `undefined`/null rows are treated as "still loading". */
  rows: R[] | undefined | null
  loading?: boolean
  onOpen: (row: R) => void
  emptyState?: { icon?: LucideIcon; title: string; description?: string }
}

const SKELETON_WIDTHS = ['w-40', 'w-24', 'w-32', 'w-20', 'w-28', 'w-36']

export function ListPage<R>({
  title,
  description,
  newButton,
  searchPlaceholder = 'Search…',
  columns,
  rows,
  loading = false,
  onOpen,
  emptyState,
}: ListPageProps<R>) {
  const [filterText, setFilterText] = useState('')
  const q = filterText.trim().toLowerCase()

  const all = rows ?? []
  const visible = q
    ? all.filter((row) => {
        const o = row as Record<string, unknown>
        return [o.name, o.slug].some((v) => typeof v === 'string' && v.toLowerCase().includes(q))
      })
    : all

  const cell = (row: R, col: ListPageColumn<R>): ReactNode => {
    if (col.render) return col.render(row)
    const v = (row as Record<string, unknown>)[col.key]
    return typeof v === 'string' || typeof v === 'number' ? <span className="text-ink">{v}</span> : null
  }

  return (
    <div className="flex flex-col">
      <PageHeader title={title} description={description} />

      <div className="mt-3 flex items-center gap-2 pb-2.5">
        <Input
          size="sm"
          variant="outline"
          icon={<Search size={13} />}
          placeholder={searchPlaceholder}
          value={filterText}
          onChange={(e) => setFilterText(e.target.value)}
          className="max-w-64"
        />
        {q && (
          <span className="text-2xs text-ink-3">
            {visible.length} of {all.length}
          </span>
        )}
        <div className="ml-auto">
          <Button size="sm" variant="primary" icon={<Plus />} onClick={newButton.onClick}>
            {newButton.label}
          </Button>
        </div>
      </div>

      <div className="overflow-hidden rounded-lg border border-line bg-surface">
        <table className="w-full border-collapse text-left text-base">
          <thead>
            <tr className="border-b border-line">
              {columns.map((col) => (
                <th
                  key={col.key}
                  className={cx('px-3 py-2 text-xs font-medium text-ink-3', col.width)}
                >
                  {col.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody className="divide-y divide-line">
            {loading
              ? SKELETON_WIDTHS.map((w, i) => (
                  <tr key={i}>
                    <td colSpan={columns.length} className="px-3 py-2.5">
                      <Skeleton className={cx('h-3.5', w)} />
                    </td>
                  </tr>
                ))
              : visible.map((row, i) => {
                  const id = (row as Record<string, unknown>).id
                  return (
                    <tr
                      key={typeof id === 'string' || typeof id === 'number' ? String(id) : i}
                      onClick={() => onOpen(row)}
                      className="cursor-pointer transition-colors duration-100 hover:bg-app"
                    >
                      {columns.map((col) => (
                        <td key={col.key} className={cx('px-3 py-2 align-middle text-ink-2', col.width)}>
                          {cell(row, col)}
                        </td>
                      ))}
                    </tr>
                  )
                })}
          </tbody>
        </table>

        {!loading && visible.length === 0 && (
          <EmptyState
            icon={q ? Search : (emptyState?.icon ?? Inbox)}
            title={q ? `No results for “${filterText.trim()}”` : (emptyState?.title ?? 'Nothing here yet')}
            description={q ? undefined : (emptyState?.description ?? 'Create the first one to get started.')}
            className="py-8"
          />
        )}
      </div>
    </div>
  )
}
