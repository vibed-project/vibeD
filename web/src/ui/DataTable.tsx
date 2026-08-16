import { ReactNode, useMemo, useState } from 'react'
import { Button, EmptyState } from './primitives'
import './DataTable.css'

// A dependency-free, generic data table: client-side global search, single-column
// sort, and pagination. Powers the Applications and admin screens so every list
// gets the same affordances (sort/filter/paginate) instead of a raw
// <table>.

export interface Column<T> {
  key: string
  header: string
  sortable?: boolean
  align?: 'left' | 'right'
  // render draws the cell; when omitted the raw value at key is shown.
  render?: (row: T) => ReactNode
  // sortValue supplies the comparable value when render is custom; defaults to
  // the raw value at key.
  sortValue?: (row: T) => string | number
}

interface DataTableProps<T> {
  columns: Column<T>[]
  rows: T[]
  rowKey: (row: T) => string
  // searchText builds the haystack a row is matched against for the search box.
  searchText?: (row: T) => string
  searchPlaceholder?: string
  toolbar?: ReactNode
  pageSize?: number
  emptyTitle?: string
  emptyDescription?: string
}

type SortDir = 'asc' | 'desc'

export function DataTable<T>(props: DataTableProps<T>) {
  const { columns, rows, rowKey, searchText, searchPlaceholder = 'Search…', toolbar, pageSize = 25, emptyTitle = 'Nothing to show', emptyDescription } = props

  const [query, setQuery] = useState('')
  const [sortKey, setSortKey] = useState<string | null>(null)
  const [sortDir, setSortDir] = useState<SortDir>('asc')
  const [page, setPage] = useState(0)

  const filtered = useMemo(() => {
    if (!query.trim() || !searchText) return rows
    const q = query.trim().toLowerCase()
    return rows.filter((r) => searchText(r).toLowerCase().includes(q))
  }, [rows, query, searchText])

  const sorted = useMemo(() => {
    if (!sortKey) return filtered
    const col = columns.find((c) => c.key === sortKey)
    if (!col) return filtered
    const valueOf = (r: T): string | number => {
      if (col.sortValue) return col.sortValue(r)
      const raw = (r as Record<string, unknown>)[col.key]
      return typeof raw === 'number' ? raw : String(raw ?? '')
    }
    const dir = sortDir === 'asc' ? 1 : -1
    return [...filtered].sort((a, b) => {
      const va = valueOf(a)
      const vb = valueOf(b)
      if (va < vb) return -1 * dir
      if (va > vb) return 1 * dir
      return 0
    })
  }, [filtered, sortKey, sortDir, columns])

  const pageCount = Math.max(1, Math.ceil(sorted.length / pageSize))
  const clampedPage = Math.min(page, pageCount - 1)
  const pageRows = sorted.slice(clampedPage * pageSize, clampedPage * pageSize + pageSize)

  const onSort = (col: Column<T>) => {
    if (!col.sortable) return
    if (sortKey === col.key) {
      setSortDir((d) => (d === 'asc' ? 'desc' : 'asc'))
    } else {
      setSortKey(col.key)
      setSortDir('asc')
    }
    setPage(0)
  }

  return (
    <div className="dt">
      {(searchText || toolbar) && (
        <div className="dt-toolbar">
          {searchText && (
            <div className="dt-search">
              <span className="dt-search-icon" aria-hidden="true">⌕</span>
              <input
                className="ui-input"
                type="search"
                placeholder={searchPlaceholder}
                value={query}
                onChange={(e) => { setQuery(e.target.value); setPage(0) }}
                aria-label="Search"
              />
            </div>
          )}
          {toolbar && <div className="dt-toolbar-slot">{toolbar}</div>}
        </div>
      )}

      {sorted.length === 0 ? (
        <div className="dt-scroll dt-empty">
          <EmptyState title={emptyTitle} description={emptyDescription} />
        </div>
      ) : (
        <div className="dt-scroll">
          <table className="dt-table">
            <thead>
              <tr>
                {columns.map((col) => {
                  const active = sortKey === col.key
                  return (
                    <th
                      key={col.key}
                      className={[col.sortable && 'dt-th-sortable', col.align === 'right' && 'dt-th-right'].filter(Boolean).join(' ')}
                      onClick={() => onSort(col)}
                      aria-sort={active ? (sortDir === 'asc' ? 'ascending' : 'descending') : undefined}
                      scope="col"
                    >
                      {col.header}
                      {col.sortable && active && <span className="dt-sort-caret" aria-hidden="true">{sortDir === 'asc' ? '▲' : '▼'}</span>}
                    </th>
                  )
                })}
              </tr>
            </thead>
            <tbody>
              {pageRows.map((row) => (
                <tr key={rowKey(row)}>
                  {columns.map((col) => (
                    <td key={col.key} className={col.align === 'right' ? 'dt-td-right' : undefined}>
                      {col.render ? col.render(row) : String((row as Record<string, unknown>)[col.key] ?? '')}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {sorted.length > pageSize && (
        <div className="dt-footer">
          <span>{sorted.length} {sorted.length === 1 ? 'row' : 'rows'}</span>
          <div className="dt-pager">
            <Button size="sm" variant="ghost" onClick={() => setPage((p) => Math.max(0, p - 1))} disabled={clampedPage === 0}>Prev</Button>
            <span className="dt-pageinfo">Page {clampedPage + 1} of {pageCount}</span>
            <Button size="sm" variant="ghost" onClick={() => setPage((p) => Math.min(pageCount - 1, p + 1))} disabled={clampedPage >= pageCount - 1}>Next</Button>
          </div>
        </div>
      )}
    </div>
  )
}
