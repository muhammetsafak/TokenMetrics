<script lang="ts" module>
  export interface Column<R> {
    key: string
    label: string
    align?: 'left' | 'right'
    /** Sort value; null sorts last. */
    value: (row: R) => string | number | null
    /** Display text; defaults to the sort value. */
    text?: (row: R) => string
    /** Tooltip of the column header (what the column means). */
    hint?: string
    /** Tooltip with the unabridged value. */
    title?: (row: R) => string | undefined
    /** Cells of this column open the row (needs onRowClick). */
    primary?: boolean
  }
</script>

<script lang="ts" generics="R">
  interface Props {
    columns: Column<R>[]
    rows: R[]
    rowKey: (row: R) => string
    /** Names the table for assistive technology. */
    caption: string
    onRowClick?: (row: R) => void
    selectedKey?: string | null
    defaultSort?: { key: string; dir: 'asc' | 'desc' }
    /** Text per column key for a last "total" row. */
    footer?: Record<string, string>
  }
  let { columns, rows, rowKey, caption, onRowClick, selectedKey = null, defaultSort, footer }: Props = $props()

  // svelte-ignore state_referenced_locally
  let sortKey = $state(defaultSort?.key ?? '')
  // svelte-ignore state_referenced_locally
  let sortDir = $state<'asc' | 'desc'>(defaultSort?.dir ?? 'desc')

  function toggle(key: string) {
    if (sortKey === key) {
      sortDir = sortDir === 'asc' ? 'desc' : 'asc'
    } else {
      sortKey = key
      sortDir = 'desc'
    }
  }

  const sorted = $derived.by(() => {
    const col = columns.find((c) => c.key === sortKey)
    if (!col) return rows
    const sign = sortDir === 'asc' ? 1 : -1
    return [...rows].sort((a, b) => {
      const x = col.value(a)
      const y = col.value(b)
      if (x === null && y === null) return 0
      if (x === null) return 1
      if (y === null) return -1
      if (typeof x === 'number' && typeof y === 'number') return (x - y) * sign
      return String(x).localeCompare(String(y), 'tr') * sign
    })
  })

  const display = (col: Column<R>, row: R) => (col.text ? col.text(row) : String(col.value(row) ?? '—'))
</script>

<!-- A scrollable region must be reachable by keyboard so it can be scrolled. -->
<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<div class="scroll" role="region" aria-label={caption} tabindex="0">
  <table>
    <caption class="sr-only">{caption}</caption>
    <thead>
      <tr>
        {#each columns as col (col.key)}
          <th
            scope="col"
            class:right={col.align === 'right'}
            aria-sort={sortKey === col.key ? (sortDir === 'asc' ? 'ascending' : 'descending') : 'none'}
          >
            <button type="button" class="sort" title={col.hint} onclick={() => toggle(col.key)}>
              {col.label}
              <span class="arrow" aria-hidden="true">{sortKey === col.key ? (sortDir === 'asc' ? '▲' : '▼') : ''}</span>
            </button>
          </th>
        {/each}
      </tr>
    </thead>
    <tbody>
      {#each sorted as row (rowKey(row))}
        <tr class:selected={selectedKey !== null && selectedKey === rowKey(row)}>
          {#each columns as col (col.key)}
            <td class:right={col.align === 'right'} title={col.title?.(row)}>
              {#if col.primary && onRowClick}
                <button
                  type="button"
                  class="open"
                  aria-pressed={selectedKey !== null ? selectedKey === rowKey(row) : undefined}
                  onclick={() => onRowClick(row)}>{display(col, row)}</button
                >
              {:else}
                {display(col, row)}
              {/if}
            </td>
          {/each}
        </tr>
      {/each}
    </tbody>
    {#if footer}
      <tfoot>
        <tr>
          {#each columns as col, i (col.key)}
            <td class:right={col.align === 'right'}>{footer[col.key] ?? (i === 0 ? 'Toplam' : '')}</td>
          {/each}
        </tr>
      </tfoot>
    {/if}
  </table>
</div>

<style>
  .scroll {
    overflow-x: auto;
  }
  table {
    width: 100%;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    padding: 10px 12px;
    text-align: left;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
    vertical-align: middle;
  }
  tbody tr:last-child td {
    border-bottom: 0;
  }
  tbody tr:hover td {
    background: var(--hover);
  }
  thead th {
    padding: 0;
    font-size: 12px;
    font-weight: 500;
    color: var(--text-3);
  }
  .right {
    text-align: right;
  }
  td.right {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .sort {
    display: flex;
    align-items: center;
    gap: 4px;
    width: 100%;
    padding: 10px 12px;
    background: none;
    border: 0;
    font-weight: inherit;
    color: inherit;
    text-align: inherit;
  }
  th.right .sort {
    justify-content: flex-end;
  }
  .sort:hover {
    background: var(--hover);
    color: var(--text-2);
  }
  /* The scroll container would clip an outset outline. */
  .sort:focus-visible {
    outline-offset: -2px;
  }
  .arrow {
    display: inline-block;
    width: 1em;
    font-size: 0.65rem;
  }
  .open {
    padding: 0;
    background: none;
    border: 0;
    color: var(--accent);
    font-weight: 500;
    text-decoration: underline;
    text-underline-offset: 3px;
    text-align: left;
  }
  .open:focus-visible {
    outline-offset: 1px;
  }
  tr.selected td {
    background: var(--hover);
  }
  tr.selected td:first-child {
    box-shadow: inset 3px 0 0 var(--accent);
  }
  tfoot td {
    font-weight: 600;
    border-top: 1px solid var(--border-strong);
    border-bottom: 0;
  }
</style>
