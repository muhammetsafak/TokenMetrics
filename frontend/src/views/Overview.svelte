<script lang="ts">
  import {
    api,
    type BreakdownData,
    type SummaryData,
    type TimeSeriesData,
  } from '../lib/api'
  import { assignColors, chartTheme } from '../lib/colors'
  import { costHint, costSortValue, costText, hasTotalOnly } from '../lib/cost'
  import {
    dimensionLabel,
    escapeHtml,
    formatBucket,
    formatInt,
    formatTokens,
    spansDays,
  } from '../lib/format'
  import {
    apiFilter,
    bucketFor,
    createLoader,
    dataVersion,
    meta,
    sourceLabels,
  } from '../lib/stores'
  import Chart from '../components/Chart.svelte'
  import CostNotes from '../components/CostNotes.svelte'
  import DataTable, { type Column } from '../components/DataTable.svelte'
  import CostValue from '../components/CostValue.svelte'
  import LimitSummaryCard from '../components/LimitSummaryCard.svelte'
  import ModelCompareTable from '../components/ModelCompareTable.svelte'
  import StatCard from '../components/StatCard.svelte'
  import type { StackBy } from '../lib/api'

  type BreakdownDim = 'model' | 'service' | 'source'

  const summary = createLoader<SummaryData>()
  const series = createLoader<TimeSeriesData>()
  const breakdown = createLoader<BreakdownData>()

  let stackBy = $state<StackBy>('service')
  let dim = $state<BreakdownDim>('model')
  let seriesAsTable = $state(false)
  let breakdownAsTable = $state(false)

  const firstRecordMs = $derived($meta?.firstRecordMs ?? 0)
  // Claude Code used with an API key shows up in both the local log and the Admin API.
  const doubleCountPossible = $derived.by(() => {
    const records = (id: string) => $meta?.sources.find((s) => s.id === id)?.records ?? 0
    return records('claude_code') > 0 && records('anthropic_api') > 0 && $apiFilter.sources.length === 0
  })
  const noData = $derived($meta !== null && $meta.totalRecords === 0)

  $effect(() => {
    const f = $apiFilter
    void $dataVersion
    summary.load(() => api.summary(f))
  })
  $effect(() => {
    const f = $apiFilter
    const stack = stackBy
    void $dataVersion
    const bucket = bucketFor(f.fromMs, f.toMs, firstRecordMs, Date.now())
    series.load(() => api.timeSeries(f, bucket, stack))
  })
  $effect(() => {
    const f = $apiFilter
    const d = dim
    void $dataVersion
    breakdown.load(() => api.breakdown(f, d))
  })

  /** Unfiltered key order of a dimension: it fixes every entity's color. */
  function order(d: string): string[] {
    if (d === 'service') return $meta?.services ?? []
    if (d === 'model') return $meta?.models ?? []
    if (d === 'source') return ($meta?.sources ?? []).map((s) => s.id)
    return []
  }

  const reducedMotion = () =>
    typeof matchMedia === 'function' && matchMedia('(prefers-reduced-motion: reduce)').matches

  function baseOption(t: ReturnType<typeof chartTheme>) {
    return {
      animation: !reducedMotion(),
      textStyle: { fontFamily: t.font },
      tooltip: {
        confine: true,
        backgroundColor: t.surface,
        borderColor: t.axis,
        textStyle: { color: t.text, fontSize: 12, fontFamily: t.font },
      },
    }
  }

  function seriesOption(d: TimeSeriesData): Record<string, unknown> {
    const t = chartTheme()
    const keys = order(d.stackBy)
    const colors = assignColors(
      d.series.map((s) => s.key).filter((k) => k !== ''),
      keys,
      d.otherKey || '__other__',
    )
    const withDate = d.bucket === 'hour' && spansDays(d.buckets)
    const layerName = (key: string) =>
      key === '' ? 'Toplam' : key === d.otherKey ? 'Diğer' : dimensionLabel(d.stackBy, key, $sourceLabels)
    const stacked = d.stackBy !== ''
    // Index of the last series with a value above zero in each bucket (the top of the stack).
    const topLayer = d.buckets.map((_, i) => d.series.reduce((top, s, si) => (s.values[i] > 0 ? si : top), -1))
    return {
      ...baseOption(t),
      grid: { left: 8, right: 8, top: stacked ? 38 : 12, bottom: 8, containLabel: true },
      legend: stacked
        ? {
            top: 0,
            right: 0,
            icon: 'roundRect',
            itemWidth: 12,
            itemHeight: 12,
            itemGap: 14,
            textStyle: { color: t.textSecondary, fontFamily: t.font, fontSize: 12 },
            type: 'scroll',
          }
        : undefined,
      tooltip: {
        ...baseOption(t).tooltip,
        trigger: 'axis',
        axisPointer: { type: 'shadow' },
        formatter: (params: { axisValueLabel: string; marker: string; seriesName: string; value: number }[]) => {
          const rows = params.filter((p) => p.value > 0)
          const total = rows.reduce((n, p) => n + p.value, 0)
          const lines = rows
            .map((p) => `${p.marker}${escapeHtml(p.seriesName)}: <b>${formatInt(p.value)}</b>`)
            .join('<br/>')
          const head = `<b>${escapeHtml(params[0]?.axisValueLabel ?? '')}</b>`
          const foot = rows.length > 1 ? `<br/>Toplam: <b>${formatInt(total)}</b>` : ''
          return `${head}<br/>${lines || 'Kayıt yok'}${foot}`
        },
      },
      xAxis: {
        type: 'category',
        data: d.buckets.map((b) => formatBucket(b, d.bucket, withDate)),
        axisLine: { lineStyle: { color: t.axis } },
        axisTick: { show: false },
        axisLabel: { color: t.textSecondary, fontFamily: t.font, fontSize: 12, hideOverlap: true },
      },
      yAxis: {
        type: 'value',
        axisLabel: { color: t.textSecondary, fontFamily: t.fontMono, fontSize: 11, formatter: (v: number) => formatTokens(v) },
        splitLine: { lineStyle: { color: t.grid } },
      },
      series: d.series.map((s, si) => ({
        name: layerName(s.key),
        type: 'bar',
        stack: 'tokens',
        barMaxWidth: 36,
        barCategoryGap: '30%',
        // Only the topmost visible segment of each stack gets the rounded corners.
        data: s.values.map((v, i) => (si === topLayer[i] ? { value: v, itemStyle: { borderRadius: [3, 3, 0, 0] } } : v)),
        itemStyle: {
          color: s.key === '' ? t.accent : colors.get(s.key),
          borderColor: t.surface,
          borderWidth: 1,
        },
        emphasis: { focus: 'series' },
      })),
    }
  }

  function breakdownOption(d: BreakdownData): Record<string, unknown> {
    const t = chartTheme()
    const keys = order(d.dimension)
    const rows = d.rows.slice(0, 10)
    const colors = assignColors(
      rows.map((r) => r.key),
      keys,
    )
    return {
      ...baseOption(t),
      grid: { left: 8, right: 56, top: 8, bottom: 8, containLabel: true },
      tooltip: {
        ...baseOption(t).tooltip,
        trigger: 'item',
        formatter: (p: { dataIndex: number }) => {
          const r = rows[p.dataIndex]
          if (!r) return ''
          const x = r.totals
          return [
            `<b>${escapeHtml(dimensionLabel(d.dimension, r.key, $sourceLabels))}</b>`,
            `Toplam: <b>${formatInt(x.totalTokens)}</b>`,
            `Girdi ${formatInt(x.inputTokens)} · Çıktı ${formatInt(x.outputTokens)}`,
            ...(x.totalOnlyTokens > 0 ? [`Bölünmemiş (yalnız toplam): ${formatInt(x.totalOnlyTokens)}`] : []),
            `Cache okuma ${formatInt(x.cacheReadTokens)} · yazma ${formatInt(x.cacheWriteTokens)}`,
            `Tahmini maliyet: ${escapeHtml(costText(x))}`,
          ].join('<br/>')
        },
      },
      xAxis: {
        type: 'value',
        axisLabel: { color: t.textSecondary, fontFamily: t.fontMono, fontSize: 11, formatter: (v: number) => formatTokens(v) },
        splitLine: { lineStyle: { color: t.grid } },
      },
      yAxis: {
        type: 'category',
        inverse: true,
        data: rows.map((r) => dimensionLabel(d.dimension, r.key, $sourceLabels)),
        axisLine: { lineStyle: { color: t.axis } },
        axisTick: { show: false },
        axisLabel: { color: t.textSecondary, fontFamily: t.font, fontSize: 12, width: 150, overflow: 'truncate' },
      },
      series: [
        {
          type: 'bar',
          barMaxWidth: 18,
          barCategoryGap: '30%',
          data: rows.map((r) => ({
            value: r.totals.totalTokens,
            itemStyle: { color: colors.get(r.key), borderRadius: [0, 3, 3, 0] },
          })),
          label: {
            show: true,
            position: 'right',
            color: t.textSecondary,
            fontFamily: t.fontMono,
            fontSize: 11,
            formatter: (p: { value: number }) => formatTokens(p.value),
          },
        },
      ],
    }
  }

  const DIM_LABEL: Record<BreakdownDim, string> = { model: 'Model', service: 'Servis', source: 'Kaynak' }

  const costTotals = $derived($summary.data ? [$summary.data.today, $summary.data.last7d, $summary.data.last30d, $summary.data.selected] : [])

  // Table views of the two charts.
  const seriesTable = $derived.by(() => {
    const d = $series.data
    if (!d) return { columns: [] as Column<number>[], rows: [] as number[] }
    const cols: Column<number>[] = [
      {
        key: 'time',
        label: 'Zaman',
        value: (i) => d.buckets[i],
        text: (i) => formatBucket(d.buckets[i], d.bucket, d.bucket === 'hour' && spansDays(d.buckets)),
      },
      ...d.series.map(
        (s, n): Column<number> => ({
          key: `s${n}`,
          label: s.key === '' ? 'Toplam token' : s.key === d.otherKey ? 'Diğer' : dimensionLabel(d.stackBy, s.key, $sourceLabels),
          align: 'right',
          value: (i) => s.values[i],
          text: (i) => formatInt(s.values[i]),
        }),
      ),
    ]
    return { columns: cols, rows: d.buckets.map((_, i) => i) }
  })

  const breakdownColumns = $derived.by((): Column<BreakdownData['rows'][number]>[] => {
    const d = $breakdown.data?.dimension ?? dim
    const unsplit = hasTotalOnly(($breakdown.data?.rows ?? []).map((r) => r.totals))
    return [
      { key: 'key', label: DIM_LABEL[dim], value: (r) => dimensionLabel(d, r.key, $sourceLabels) },
      { key: 'total', label: 'Token', align: 'right', value: (r) => r.totals.totalTokens, text: (r) => formatInt(r.totals.totalTokens) },
      { key: 'in', label: 'Girdi', align: 'right', value: (r) => r.totals.inputTokens, text: (r) => formatInt(r.totals.inputTokens) },
      { key: 'out', label: 'Çıktı', align: 'right', value: (r) => r.totals.outputTokens, text: (r) => formatInt(r.totals.outputTokens) },
      ...(unsplit
        ? [
            {
              key: 'unsplit',
              label: 'Bölünmemiş',
              align: 'right' as const,
              value: (r: BreakdownData['rows'][number]) => r.totals.totalOnlyTokens,
              text: (r: BreakdownData['rows'][number]) => formatInt(r.totals.totalOnlyTokens),
              hint: 'Girdi/çıktı ayrımı olmayan, yalnız toplam token yazan kayıtlar',
            },
          ]
        : []),
      {
        key: 'cost',
        label: 'Tahmini $',
        align: 'right',
        value: (r) => costSortValue(r.totals),
        text: (r) => costText(r.totals),
        title: (r) => costHint(r.totals),
      },
    ]
  })

  const BUCKET_LABEL: Record<string, string> = { hour: 'Saatlik', day: 'Günlük', week: 'Haftalık', month: 'Aylık' }
  const STACK_LABEL: Record<string, string> = { '': 'Toplam', service: 'Servise göre', model: 'Modele göre', source: 'Kaynağa göre' }
  const seriesDesc = $derived(
    $series.data ? `${BUCKET_LABEL[$series.data.bucket] ?? $series.data.bucket} · ${STACK_LABEL[$series.data.stackBy] ?? ''}` : '',
  )

  const seriesLabel = $derived(
    `Seçili aralıkta zamana göre token kullanımı${stackBy ? `, ${DIM_LABEL[stackBy]} bazında yığılmış` : ''}`,
  )
</script>

<div class="view">
  {#if $meta !== null && noData}
    <section class="card state" aria-live="polite">
      <h2>Veri yok</h2>
      <p>Veri yok — kaynakları Ayarlar'dan açın. Etkin kaynaklar taranınca kullanım burada görünür.</p>
      <ul class="srcs">
        {#each $meta.sources.filter((s) => s.collector) as s (s.id)}
          <li>
            <strong>{s.label}</strong>:
            {#if s.active}etkin{:else if !s.enabled}kapalı{:else}etkin değil (Admin API anahtarı yok){/if}
          </li>
        {/each}
      </ul>
    </section>
  {:else}
    <div class="cards" aria-busy={$summary.status === 'loading'}>
      {#if $summary.status === 'error'}
        <section class="card state error span" role="alert">
          <h2>Özet yüklenemedi</h2>
          <p>{$summary.error}</p>
          <button type="button" class="btn" onclick={() => summary.load(() => api.summary($apiFilter))}>Yeniden dene</button>
        </section>
      {:else if $summary.data}
        <StatCard label="Bugün" totals={$summary.data.today} />
        <StatCard label="Son 7 gün" totals={$summary.data.last7d} />
        <StatCard label="Son 30 gün" totals={$summary.data.last30d} />
      {:else}
        <p class="muted span" aria-busy="true">Yükleniyor…</p>
      {/if}
      <LimitSummaryCard />
    </div>
    {#if $summary.data}
      <p class="selected muted">
        Seçili aralık:
        <strong class="num">{formatTokens($summary.data.selected.totalTokens)} token</strong>
        · <CostValue totals={$summary.data.selected} />
        · <span class="num">{formatInt($summary.data.selected.requests)}</span> istek
      </p>
      {#if $summary.data.selected.records === 0}
        <section class="card state" aria-live="polite">
          <h2>Seçili filtrelerde kayıt yok</h2>
          <p>Tarih aralığını genişletin ya da filtreleri sıfırlayın. Bugün / 7 gün / 30 gün kartları tarih aralığından bağımsızdır.</p>
        </section>
      {/if}
    {/if}

    {#if $summary.data?.selected.records !== 0}
      <section class="card" aria-label="Zaman serisi">
        <div class="section-head">
          <div>
            <h2>Token kullanımı</h2>
            {#if seriesDesc}<p class="desc">{seriesDesc}</p>{/if}
          </div>
          <div class="tools">
            <div class="seg" role="group" aria-label="Yığın ölçütü">
              <button type="button" aria-pressed={stackBy === 'service'} onclick={() => (stackBy = 'service')}>Servis</button>
              <button type="button" aria-pressed={stackBy === 'model'} onclick={() => (stackBy = 'model')}>Model</button>
              <button type="button" aria-pressed={stackBy === ''} onclick={() => (stackBy = '')}>Toplam</button>
            </div>
            <div class="seg" role="group" aria-label="Görünüm">
              <button type="button" aria-pressed={!seriesAsTable} onclick={() => (seriesAsTable = false)}>Grafik</button>
              <button type="button" aria-pressed={seriesAsTable} onclick={() => (seriesAsTable = true)}>Tablo</button>
            </div>
          </div>
        </div>
        {#if $series.status === 'error'}
          <p class="err" role="alert">Zaman serisi yüklenemedi: {$series.error}</p>
        {:else if $series.data}
          {#if seriesAsTable}
            <DataTable
              columns={seriesTable.columns}
              rows={seriesTable.rows}
              rowKey={(i) => String(i)}
              caption="Zamana göre token kullanımı"
              defaultSort={{ key: 'time', dir: 'desc' }}
            />
          {:else}
            <Chart option={() => seriesOption($series.data!)} label={seriesLabel} height={300} />
          {/if}
        {:else}
          <p class="muted" aria-busy="true">Yükleniyor…</p>
        {/if}
      </section>

      <section class="card" aria-label="Kırılım">
        <div class="section-head">
          <h2>Kırılım <span class="muted sub">(en çok kullanan 10)</span></h2>
          <div class="tools">
            <div class="seg" role="group" aria-label="Kırılım ölçütü">
              <button type="button" aria-pressed={dim === 'model'} onclick={() => (dim = 'model')}>Model</button>
              <button type="button" aria-pressed={dim === 'service'} onclick={() => (dim = 'service')}>Servis</button>
              <button type="button" aria-pressed={dim === 'source'} onclick={() => (dim = 'source')}>Kaynak</button>
            </div>
            <div class="seg" role="group" aria-label="Görünüm">
              <button type="button" aria-pressed={!breakdownAsTable} onclick={() => (breakdownAsTable = false)}>Grafik</button>
              <button type="button" aria-pressed={breakdownAsTable} onclick={() => (breakdownAsTable = true)}>Tablo</button>
            </div>
          </div>
        </div>
        {#if $breakdown.status === 'error'}
          <p class="err" role="alert">Kırılım yüklenemedi: {$breakdown.error}</p>
        {:else if $breakdown.data}
          {#if breakdownAsTable}
            <DataTable
              columns={breakdownColumns}
              rows={$breakdown.data.rows}
              rowKey={(r) => r.key}
              caption="{DIM_LABEL[dim]} bazında token kırılımı"
              defaultSort={{ key: 'total', dir: 'desc' }}
            />
          {:else}
            <Chart
              option={() => breakdownOption($breakdown.data!)}
              label="{DIM_LABEL[dim]} bazında token kullanımı"
              height={Math.max(120, 28 + Math.min($breakdown.data.rows.length, 10) * 32)}
            />
          {/if}
        {:else}
          <p class="muted" aria-busy="true">Yükleniyor…</p>
        {/if}
      </section>

      <ModelCompareTable />
    {/if}

    {#if $summary.data}
      <CostNotes totals={costTotals} asOf={$summary.data.priceAsOf} />
      {#if doubleCountPossible}
        <p class="muted note" role="note">
          Not: Claude Code'u API anahtarıyla kullanıyorsanız aynı tüketim hem yerel log'da hem Anthropic Admin API'de
          görünür ve toplamlar çift sayılır. Kaynak filtresiyle birini seçin.
        </p>
      {/if}
    {/if}
  {/if}
</div>

<style>
  .cards {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(min(240px, 100%), 1fr));
    gap: 16px;
  }
  .span {
    grid-column: 1 / -1;
  }
  .selected {
    font-size: 0.9rem;
  }
  .selected strong {
    color: var(--text);
    font-weight: 500;
  }
  .selected :global(.cost-text),
  .selected .num {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .desc {
    margin-top: 2px;
    font-size: 12px;
    color: var(--text-3);
  }
  .tools {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
  }
  .sub {
    font-size: 0.8rem;
    font-weight: 400;
  }
  .err {
    color: var(--critical-text);
  }
  .note {
    font-size: 0.8rem;
  }
  .srcs {
    margin: 0;
    padding-left: 18px;
  }
</style>
