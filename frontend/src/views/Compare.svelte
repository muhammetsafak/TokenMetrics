<script lang="ts">
  import { api, type CompareData, type CompareRow } from '../lib/api'
  import { costHint, costSortValue, costSumText, costText, hasTotalOnly } from '../lib/cost'
  import { formatInt, modelLabel, serviceLabel } from '../lib/format'
  import { apiFilter, createLoader, dataVersion, meta } from '../lib/stores'
  import CostNotes from '../components/CostNotes.svelte'
  import DataTable, { type Column } from '../components/DataTable.svelte'
  import EstimatedBadge from '../components/EstimatedBadge.svelte'

  const compare = createLoader<CompareData>()

  $effect(() => {
    const f = $apiFilter
    void $dataVersion
    compare.load(() => api.compare(f))
  })

  const int = (pick: (r: CompareRow) => number, label: string, key: string): Column<CompareRow> => ({
    key,
    label,
    align: 'right',
    value: pick,
    text: (r) => formatInt(pick(r)),
  })

  const unsplit = $derived(hasTotalOnly(($compare.data?.rows ?? []).map((r) => r.totals)))

  const columns = $derived<Column<CompareRow>[]>([
    { key: 'service', label: 'Servis', value: (r) => serviceLabel(r.service) },
    { key: 'model', label: 'Model', value: (r) => modelLabel(r.model), title: (r) => r.model },
    int((r) => r.totals.inputTokens, 'Girdi', 'in'),
    int((r) => r.totals.outputTokens, 'Çıktı', 'out'),
    ...(unsplit
      ? [{ ...int((r) => r.totals.totalOnlyTokens, 'Bölünmemiş', 'unsplit'), hint: 'Girdi/çıktı ayrımı olmayan, yalnız toplam token yazan kayıtlar' }]
      : []),
    int((r) => r.totals.cacheReadTokens, 'Cache okuma', 'cr'),
    int((r) => r.totals.cacheWriteTokens, 'Cache yazma', 'cw'),
    int((r) => r.totals.thinkingTokens, 'Thinking', 'think'),
    int((r) => r.totals.requests, 'İstek', 'req'),
    {
      key: 'cost',
      label: 'Tahmini $',
      align: 'right',
      value: (r) => costSortValue(r.totals),
      text: (r) => costText(r.totals),
      title: (r) => costHint(r.totals),
    },
  ])

  const footer = $derived.by(() => {
    const rows = $compare.data?.rows ?? []
    const sum = (pick: (r: CompareRow) => number) => formatInt(rows.reduce((n, r) => n + pick(r), 0))
    return {
      service: 'Toplam',
      in: sum((r) => r.totals.inputTokens),
      out: sum((r) => r.totals.outputTokens),
      unsplit: sum((r) => r.totals.totalOnlyTokens),
      cr: sum((r) => r.totals.cacheReadTokens),
      cw: sum((r) => r.totals.cacheWriteTokens),
      think: sum((r) => r.totals.thinkingTokens),
      req: sum((r) => r.totals.requests),
      cost: costSumText(rows.map((r) => r.totals)),
    }
  })
</script>

<div class="view">
  <section class="card" aria-label="Servis ve model karşılaştırması">
    <div class="section-head">
      <h2>Servis × model <EstimatedBadge title="Maliyet sütunu fiyat tablosundan hesaplanmış tahmini değerdir" /></h2>
    </div>
    {#if $compare.status === 'error'}
      <div class="state error" role="alert">
        <h2>Karşılaştırma yüklenemedi</h2>
        <p>{$compare.error}</p>
        <button type="button" class="btn" onclick={() => compare.load(() => api.compare($apiFilter))}>Yeniden dene</button>
      </div>
    {:else if !$compare.data}
      <p class="muted" aria-busy="true">Yükleniyor…</p>
    {:else if $compare.data.rows.length === 0}
      <div class="state" aria-live="polite">
        {#if $meta !== null && $meta.totalRecords === 0}
          <h2>Veri yok</h2>
          <p>Veri yok — kaynakları Ayarlar'dan açın.</p>
        {:else}
          <h2>Seçili filtrelerde kayıt yok</h2>
          <p>Tarih aralığını genişletin ya da filtreleri sıfırlayın.</p>
        {/if}
      </div>
    {:else}
      <DataTable
        {columns}
        rows={$compare.data.rows}
        rowKey={(r) => `${r.service}\u0000${r.model}`}
        caption="Servis ve model bazında token ve maliyet"
        defaultSort={{ key: 'cost', dir: 'desc' }}
        {footer}
      />
    {/if}
  </section>
  {#if $compare.data && $compare.data.rows.length > 0}
    <CostNotes totals={$compare.data.rows.map((r) => r.totals)} asOf={$compare.data.priceAsOf} />
  {/if}
</div>
