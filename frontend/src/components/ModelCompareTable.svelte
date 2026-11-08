<script lang="ts">
  import { api, type CompareData, type CompareRow } from '../lib/api'
  import { assignColors } from '../lib/colors'
  import { costHint, costText } from '../lib/cost'
  import { formatInt, formatPercent, formatTokens, modelLabel, serviceLabel } from '../lib/format'
  import { apiFilter, createLoader, dataVersion, meta, themeTick, view } from '../lib/stores'
  import EstimatedBadge from './EstimatedBadge.svelte'

  const MAX_ROWS = 8

  const compare = createLoader<CompareData>()

  $effect(() => {
    const f = $apiFilter
    void $dataVersion
    compare.load(() => api.compare(f))
  })

  const sorted = $derived(
    [...($compare.data?.rows ?? [])].sort((a, b) => b.totals.totalTokens - a.totals.totalTokens),
  )
  const shown = $derived(sorted.slice(0, MAX_ROWS))
  const grandTotal = $derived(sorted.reduce((n, r) => n + r.totals.totalTokens, 0))
  const multiService = $derived(new Set(sorted.map((r) => r.service)).size > 1)

  // Models with a slot (position < 8) in the unfiltered model list keep the color they have in the charts.
  // The visible list is deduplicated: one model can appear under several services.
  const colors = $derived.by(() => {
    void $themeTick
    return assignColors([...new Set(sorted.map((r) => r.model))], $meta?.models ?? [])
  })

  const share = (r: CompareRow) => (grandTotal > 0 ? r.totals.totalTokens / grandTotal : 0)
  const cache = (r: CompareRow) => r.totals.cacheReadTokens + r.totals.cacheWriteTokens
  const unsplitHint = (r: CompareRow) =>
    r.totals.totalOnlyTokens > 0
      ? `${formatInt(r.totals.totalOnlyTokens)} token bölünmemiş (yalnız toplam): girdi/çıktı ayrımı yok`
      : undefined
  const rowKey = (r: CompareRow) => `${r.service}\u0000${r.model}`
</script>

<section class="card" aria-label="Model karşılaştırması">
  <div class="section-head">
    <div>
      <h2>Model karşılaştırması</h2>
      <p class="desc">Seçili aralıkta modele göre token ve maliyet</p>
    </div>
  </div>

  {#if $compare.status === 'error'}
    <div class="state error" role="alert">
      <h3 class="state-title">Karşılaştırma yüklenemedi</h3>
      <p>{$compare.error}</p>
      <button type="button" class="btn" onclick={() => compare.load(() => api.compare($apiFilter))}>Yeniden dene</button>
    </div>
  {:else if !$compare.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else if sorted.length === 0}
    <div class="state" aria-live="polite">
      <h3 class="state-title">Seçili filtrelerde kayıt yok</h3>
      <p>Tarih aralığını genişletin ya da filtreleri sıfırlayın.</p>
    </div>
  {:else}
    <div class="wrap" aria-busy={$compare.status === 'loading'}>
      <table>
        <caption class="sr-only">Model bazında token ve tahmini maliyet</caption>
        <thead>
          <tr>
            <th scope="col" class="left">Model</th>
            <th scope="col" class="right">Girdi</th>
            <th scope="col" class="right">Çıktı</th>
            <th scope="col" class="right">Cache</th>
            <th scope="col" class="right">İstek</th>
            <th scope="col" class="left">Pay</th>
            <th scope="col" class="right">Maliyet <EstimatedBadge title="Maliyet sütunu fiyat tablosundan hesaplanmış tahmini değerdir" /></th>
          </tr>
        </thead>
        <tbody>
          {#each shown as r (rowKey(r))}
            <tr>
              <th scope="row" class="left model" title={unsplitHint(r) ? `${r.model}\n${unsplitHint(r)}` : r.model}>
                <span class="swatch" style:background={colors.get(r.model)} aria-hidden="true"></span>
                <span class="mname">{modelLabel(r.model)}</span>
                {#if multiService}<span class="svc">{serviceLabel(r.service)}</span>{/if}
                {#if r.totals.totalOnlyTokens > 0}
                  <span class="unsplit">{formatTokens(r.totals.totalOnlyTokens)} token bölünmemiş (yalnız toplam)</span>
                {/if}
              </th>
              <td class="right num">{formatInt(r.totals.inputTokens)}</td>
              <td class="right num">{formatInt(r.totals.outputTokens)}</td>
              <td class="right num">{formatInt(cache(r))}</td>
              <td class="right num">{formatInt(r.totals.requests)}</td>
              <td class="left">
                <span class="share">
                  <span class="bar" aria-hidden="true"><span class="barfill" style:width="{share(r) * 100}%"></span></span>
                  <span class="num pct">{formatPercent(share(r))}</span>
                </span>
              </td>
              <td class="right num" title={costHint(r.totals)}>{costText(r.totals)}</td>
            </tr>
          {/each}
        </tbody>
      </table>
    </div>
    {#if sorted.length > shown.length}
      <button type="button" class="btn all" onclick={() => view.set('compare')}>
        Tümünü Karşılaştırma'da gör ({sorted.length})
      </button>
    {/if}
  {/if}
</section>

<style>
  .desc {
    margin-top: 2px;
    font-size: 12px;
    color: var(--text-3);
  }
  .state-title {
    color: var(--text);
    font-family: var(--font-display);
    font-size: 1.05rem;
    font-weight: 600;
  }
  .wrap {
    overflow-x: auto;
  }
  table {
    width: 100%;
    min-width: 720px;
    border-collapse: collapse;
    font-size: 13px;
  }
  th,
  td {
    padding: 10px 12px;
    border-bottom: 1px solid var(--border);
    white-space: nowrap;
    vertical-align: middle;
  }
  thead th {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-2);
  }
  tbody tr:last-child > * {
    border-bottom: 0;
  }
  .left {
    text-align: left;
  }
  .right {
    text-align: right;
  }
  .model {
    font-weight: 500;
    color: var(--text);
  }
  .swatch {
    display: inline-block;
    width: 10px;
    height: 10px;
    margin-right: 8px;
    border-radius: 3px;
    vertical-align: middle;
  }
  .mname {
    vertical-align: middle;
  }
  .svc {
    margin-left: 8px;
    font-size: 12px;
    font-weight: 400;
    color: var(--text-3);
    vertical-align: middle;
  }
  .unsplit {
    display: block;
    margin: 2px 0 0 18px;
    font-size: 12px;
    font-weight: 400;
    color: var(--text-3);
  }
  .share {
    display: inline-flex;
    align-items: center;
    gap: 8px;
  }
  .bar {
    display: inline-block;
    width: 72px;
    height: 6px;
    background: var(--surface-2);
    border-radius: 999px;
    overflow: hidden;
  }
  .barfill {
    display: block;
    height: 100%;
    min-width: 2px;
    background: var(--accent);
    border-radius: 999px;
  }
  .pct {
    min-width: 3ch;
    text-align: right;
    color: var(--text-2);
  }
  .all {
    margin-top: 12px;
  }
</style>
