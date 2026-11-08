<script lang="ts">
  import { api, type BreakdownData, type BreakdownRow } from '../lib/api'
  import { costHint, costSortValue, costText, hasTotalOnly } from '../lib/cost'
  import { formatInt, modelLabel, projectLabel, sessionLabel } from '../lib/format'
  import { apiFilter, createLoader, dataVersion, meta } from '../lib/stores'
  import CostNotes from '../components/CostNotes.svelte'
  import DataTable, { type Column } from '../components/DataTable.svelte'
  import EstimatedBadge from '../components/EstimatedBadge.svelte'

  const projects = createLoader<BreakdownData>()
  const sessions = createLoader<BreakdownData>()
  const sessionModels = createLoader<BreakdownData>()

  let selectedProject = $state<string | null>(null)
  let selectedSession = $state<string | null>(null)

  $effect(() => {
    const f = $apiFilter
    void $dataVersion
    projects.load(() => api.breakdown(f, 'project'))
  })

  // Sessions of the chosen project, under the same filter.
  // The list of another project must not show (and stay clickable) while the
  // new one loads, so the loader is reset when the project changes.
  let sessionsOf: string | null = null
  $effect(() => {
    const project = selectedProject
    const f = $apiFilter
    void $dataVersion
    if (project !== sessionsOf) {
      sessions.reset()
      sessionsOf = project
    }
    if (project === null) return
    sessions.load(() => api.sessions({ ...f, projects: [project] }, 200))
  })

  // Model breakdown of the chosen session.
  let modelsOf: string | null = null
  $effect(() => {
    const project = selectedProject
    const session = selectedSession
    const f = $apiFilter
    void $dataVersion
    const subject = project === null || session === null ? null : `${project}\u0000${session}`
    if (subject !== modelsOf) {
      sessionModels.reset()
      modelsOf = subject
    }
    if (project === null || session === null) return
    sessionModels.load(() => api.breakdown({ ...f, projects: [project], sessions: [session] }, 'model'))
  })

  // A project that vanished from the filtered list can no longer be drilled into.
  $effect(() => {
    const list = $projects.data?.rows
    if (list && selectedProject !== null && !list.some((r) => r.key === selectedProject)) {
      selectedProject = null
      selectedSession = null
    }
  })

  function openProject(row: BreakdownRow) {
    selectedProject = selectedProject === row.key ? null : row.key
    selectedSession = null
  }

  const int = (pick: (r: BreakdownRow) => number, label: string, key: string): Column<BreakdownRow> => ({
    key,
    label,
    align: 'right',
    value: pick,
    text: (r) => formatInt(pick(r)),
  })

  const costColumn: Column<BreakdownRow> = {
    key: 'cost',
    label: 'Tahmini $',
    align: 'right',
    value: (r) => costSortValue(r.totals),
    text: (r) => costText(r.totals),
    title: (r) => costHint(r.totals),
  }

  const unsplitColumn: Column<BreakdownRow> = {
    ...int((r) => r.totals.totalOnlyTokens, 'Bölünmemiş', 'unsplit'),
    hint: 'Girdi/çıktı ayrımı olmayan, yalnız toplam token yazan kayıtlar',
  }
  const withUnsplit = (cols: Column<BreakdownRow>[], data: BreakdownData | null | undefined) =>
    hasTotalOnly((data?.rows ?? []).map((r) => r.totals))
      ? cols.flatMap((c) => (c.key === 'out' ? [c, unsplitColumn] : [c]))
      : cols

  const projectColumns: Column<BreakdownRow>[] = [
    { key: 'name', label: 'Proje', primary: true, value: (r) => projectLabel(r.key), title: (r) => r.key || undefined },
    int((r) => r.totals.totalTokens, 'Token', 'total'),
    int((r) => r.totals.inputTokens, 'Girdi', 'in'),
    int((r) => r.totals.outputTokens, 'Çıktı', 'out'),
    int((r) => r.totals.requests, 'İstek', 'req'),
    costColumn,
  ]

  const sessionColumns: Column<BreakdownRow>[] = [
    { key: 'name', label: 'Oturum', primary: true, value: (r) => sessionLabel(r.key), title: (r) => r.key || undefined },
    int((r) => r.totals.totalTokens, 'Token', 'total'),
    int((r) => r.totals.inputTokens, 'Girdi', 'in'),
    int((r) => r.totals.outputTokens, 'Çıktı', 'out'),
    int((r) => r.totals.cacheReadTokens, 'Cache okuma', 'cr'),
    int((r) => r.totals.cacheWriteTokens, 'Cache yazma', 'cw'),
    int((r) => r.totals.requests, 'İstek', 'req'),
    costColumn,
  ]

  // The backend computes costs for this many rows (maxCostRows in internal/app).
  const COST_ROWS = 300

  const modelColumns: Column<BreakdownRow>[] = [
    { key: 'name', label: 'Model', value: (r) => modelLabel(r.key), title: (r) => r.key },
    int((r) => r.totals.totalTokens, 'Token', 'total'),
    unsplitColumn,
    int((r) => r.totals.requests, 'İstek', 'req'),
    costColumn,
  ]

  const modelCols = $derived(
    hasTotalOnly(($sessionModels.data?.rows ?? []).map((r) => r.totals))
      ? modelColumns
      : modelColumns.filter((c) => c.key !== 'unsplit'),
  )
  const projectCols = $derived(withUnsplit(projectColumns, $projects.data))
  const sessionCols = $derived(withUnsplit(sessionColumns, $sessions.data))

  const sessionDetail = $derived($sessions.data?.rows.find((r) => r.key === selectedSession) ?? null)
  const costTotals = $derived([...($projects.data?.rows ?? []), ...($sessions.data?.rows ?? [])].map((r) => r.totals))
</script>

<div class="view">
  <section class="card" aria-label="Proje kırılımı">
    <div class="section-head">
      <h2>Projeler <EstimatedBadge title="Maliyet sütunu fiyat tablosundan hesaplanmış tahmini değerdir" /></h2>
      <p class="muted hint">Bir projeye tıklayarak oturumlarını görün.</p>
    </div>
    {#if $projects.status === 'error'}
      <div class="state error" role="alert">
        <h2>Projeler yüklenemedi</h2>
        <p>{$projects.error}</p>
        <button type="button" class="btn" onclick={() => projects.load(() => api.breakdown($apiFilter, 'project'))}>Yeniden dene</button>
      </div>
    {:else if !$projects.data}
      <p class="muted" aria-busy="true">Yükleniyor…</p>
    {:else if $projects.data.rows.length === 0}
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
        columns={projectCols}
        rows={$projects.data.rows}
        rowKey={(r) => r.key}
        caption="Proje bazında token ve maliyet"
        defaultSort={{ key: 'total', dir: 'desc' }}
        onRowClick={openProject}
        selectedKey={selectedProject}
      />
    {/if}
    {#if $projects.data?.costTruncated}
      <p class="muted hint">Maliyet yalnız en büyük {formatInt(COST_ROWS)} proje için hesaplandı; diğerlerinde "—" görünür.</p>
    {/if}
  </section>

  {#if selectedProject !== null}
    <section class="card" aria-label="Oturumlar">
      <div class="section-head">
        <h2>Oturumlar <span class="muted sub" title={selectedProject}>{projectLabel(selectedProject)}</span></h2>
        <button type="button" class="btn" onclick={() => ((selectedProject = null), (selectedSession = null))}>Kapat</button>
      </div>
      {#if $sessions.status === 'error'}
        <p class="err" role="alert">Oturumlar yüklenemedi: {$sessions.error}</p>
      {:else if !$sessions.data}
        <p class="muted" aria-busy="true">Yükleniyor…</p>
      {:else}
        <DataTable
          columns={sessionCols}
          rows={$sessions.data.rows}
          rowKey={(r) => r.key}
          caption="Seçili projedeki oturumlar"
          defaultSort={{ key: 'total', dir: 'desc' }}
          onRowClick={(r) => (selectedSession = selectedSession === r.key ? null : r.key)}
          selectedKey={selectedSession}
        />
        {#if $sessions.data.costTruncated}
          <p class="muted hint">Maliyet yalnız ilk {formatInt(COST_ROWS)} oturum için hesaplandı; diğerlerinde "—" görünür.</p>
        {/if}
        {#if $sessions.data.truncated}
          <p class="muted hint">En büyük {formatInt($sessions.data.rows.length)} oturum gösteriliyor.</p>
        {/if}
        {#if sessionDetail}
          <dl class="detail" aria-label="Oturum detayı">
            <div class="wide"><dt>Oturum</dt><dd class="mono">{sessionDetail.key || '(oturum yok)'}</dd></div>
            <div><dt>Girdi</dt><dd>{formatInt(sessionDetail.totals.inputTokens)}</dd></div>
            <div><dt>Çıktı</dt><dd>{formatInt(sessionDetail.totals.outputTokens)}</dd></div>
            {#if sessionDetail.totals.totalOnlyTokens > 0}
              <div><dt>Bölünmemiş (yalnız toplam)</dt><dd>{formatInt(sessionDetail.totals.totalOnlyTokens)}</dd></div>
            {/if}
            <div><dt>Cache okuma</dt><dd>{formatInt(sessionDetail.totals.cacheReadTokens)}</dd></div>
            <div><dt>Cache yazma (5 dk / 1 sa)</dt><dd>{formatInt(sessionDetail.totals.cacheWrite5mTokens)} / {formatInt(sessionDetail.totals.cacheWrite1hTokens)}</dd></div>
            <div><dt>Thinking</dt><dd>{formatInt(sessionDetail.totals.thinkingTokens)}</dd></div>
            <div><dt>İstek</dt><dd>{formatInt(sessionDetail.totals.requests)}</dd></div>
            <div>
              <dt>Tahmini maliyet</dt>
              <dd>
                {#if sessionDetail.totals.costUnavailable || sessionDetail.totals.costUsd !== undefined}
                  <span title={costHint(sessionDetail.totals)}>{costText(sessionDetail.totals)}</span>
                {:else}
                  — <span class="muted">çok sayıda oturum olduğu için hesaplanmadı</span>
                {/if}
              </dd>
            </div>
          </dl>
          {#if $sessionModels.data && $sessionModels.data.rows.length > 0}
            <h3 class="models-title">Oturumdaki modeller</h3>
            <DataTable
              columns={modelCols}
              rows={$sessionModels.data.rows}
              rowKey={(r) => r.key}
              caption="Seçili oturumdaki modeller"
              defaultSort={{ key: 'total', dir: 'desc' }}
            />
          {:else if $sessionModels.status === 'error'}
            <p class="err" role="alert">Model kırılımı yüklenemedi: {$sessionModels.error}</p>
          {/if}
        {/if}
      {/if}
    </section>
  {/if}

  {#if $projects.data && $projects.data.rows.length > 0}
    <CostNotes totals={costTotals} asOf={$meta?.priceAsOf ?? ''} />
  {/if}
</div>

<style>
  .hint {
    font-size: 12px;
    color: var(--text-3);
  }
  section > .hint {
    margin-top: 10px;
  }
  .sub {
    font-family: var(--font);
    font-size: 13px;
    font-weight: 400;
  }
  .err {
    color: var(--critical-text);
  }
  .detail {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(170px, 1fr));
    gap: 12px 16px;
    margin: 16px 0 0;
    padding: 16px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
  }
  .detail div {
    min-width: 0;
  }
  .detail .wide {
    grid-column: 1 / -1;
  }
  dt {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-3);
  }
  dd {
    margin: 2px 0 0;
    overflow-wrap: anywhere;
    font-family: var(--font-mono);
    font-size: 13px;
    font-variant-numeric: tabular-nums;
  }
  .models-title {
    margin: 20px 0 8px;
    font-family: var(--font-display);
    font-size: 15px;
    font-weight: 600;
  }
  .mono {
    font-family: var(--font-mono);
    font-size: 12px;
  }
</style>
