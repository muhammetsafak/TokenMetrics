<script lang="ts">
  import { api, type LimitItem, type LimitsData } from '../lib/api'
  import { formatDateTime, LIMIT_KIND_LABELS, serviceLabel } from '../lib/format'
  import { limitLevel, limitPercentText, limitUsedText, limitValue } from '../lib/limits'
  import { createLoader, dataVersion, view } from '../lib/stores'
  import EstimatedBadge from './EstimatedBadge.svelte'

  const MAX_ROWS = 4

  const limits = createLoader<LimitsData>()

  $effect(() => {
    void $dataVersion
    limits.load(() => api.limits())
  })

  const rows = $derived.by(() => {
    const all = [...($limits.data?.limits ?? [])].sort(
      (a, b) => a.service.localeCompare(b.service) || (a.kind === b.kind ? 0 : a.kind === '5h' ? -1 : 1),
    )
    return all
  })
  const shown = $derived(rows.slice(0, MAX_ROWS))
  const services = $derived(new Set(rows.map((l) => l.service)))

  // With several services the row names the service too; with one, the window is enough.
  const rowLabel = (l: LimitItem) => {
    const kind = LIMIT_KIND_LABELS[l.kind] ?? l.kind
    return services.size > 1 ? `${l.service ? serviceLabel(l.service) : 'Tüm servisler'} · ${kind}` : kind
  }
  const key = (l: LimitItem) => `${l.service}|${l.kind}`
</script>

<section class="card limits" aria-label="Abonelik limitleri" aria-busy={$limits.status === 'loading' && !$limits.data}>
  <div class="head">
    <h3>Abonelik limitleri</h3>
    <EstimatedBadge text="yaklaşık" title="Abonelik limitleri resmî olarak yayınlanmaz; değerler tahminidir" />
  </div>

  {#if $limits.status === 'error'}
    <div class="state error" role="alert">
      <p>Limitler yüklenemedi: {$limits.error}</p>
      <button type="button" class="btn" onclick={() => limits.load(() => api.limits())}>Yeniden dene</button>
    </div>
  {:else if !$limits.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else if rows.length === 0}
    <div class="state" aria-live="polite">
      <p>Limit tanımlı değil</p>
      <button type="button" class="btn" onclick={() => view.set('limits')}>Limitler sekmesine git</button>
    </div>
  {:else}
    <ul class="rows">
      {#each shown as l (key(l))}
        {@const lower = l.missingTotalOnlyTokens > 0}
        {@const lv = limitLevel(l.ratio, lower)}
        <li>
          <div class="line">
            <span class="name">{rowLabel(l)}</span>
            <span class="pct num" class:over={lv.id === 'over'}>{limitPercentText(l)}</span>
          </div>
          <div
            class="track"
            role="progressbar"
            aria-label={rowLabel(l)}
            aria-valuemin="0"
            aria-valuemax="100"
            aria-valuenow={Math.min(100, Math.round(l.ratio * 100))}
            aria-valuetext="{limitPercentText(l)}, {lv.text}"
          >
            <div class="fill {lv.id}" style:width="{Math.min(100, l.ratio * 100)}%"></div>
          </div>
          <p class="note">
            {limitUsedText(l)} / {limitValue(l.metric, l.limit)}
            · {#if l.kind === 'weekly'}sıfırlanma {formatDateTime(l.windowEndMs)}{:else}son 5 saat (kayan){/if}
            {#if lv.id !== 'ok'}· {lv.text}{/if}
          </p>
        </li>
      {/each}
    </ul>
    {#if rows.length > shown.length}
      <button type="button" class="more" onclick={() => view.set('limits')}>
        Tümünü Limitler'de gör ({rows.length})
      </button>
    {/if}
  {/if}
</section>

<style>
  .limits {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px 8px;
  }
  .head h3 {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-2);
  }
  .state {
    padding: 4px 0;
    gap: 12px;
  }
  .rows {
    display: grid;
    gap: 14px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .line {
    display: flex;
    justify-content: space-between;
    align-items: baseline;
    gap: 8px;
  }
  .name {
    font-size: 13px;
    font-weight: 500;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .pct {
    font-size: 13px;
    font-weight: 500;
    white-space: nowrap;
  }
  .pct.over {
    color: var(--critical-text);
  }
  .track {
    height: 8px;
    margin: 6px 0;
    background: var(--surface-2);
    border-radius: 999px;
    overflow: hidden;
  }
  .fill {
    height: 100%;
    min-width: 4px;
    border-radius: 999px;
    background: var(--accent);
  }
  .fill.near {
    background: var(--warning);
  }
  .fill.unk {
    background: var(--text-3);
  }
  .fill.over {
    background: var(--critical);
  }
  .note {
    font-size: 12px;
    color: var(--text-3);
  }
  .more {
    align-self: flex-start;
    padding: 4px 0;
    border: 0;
    background: none;
    color: var(--accent);
    font-size: 13px;
    font-weight: 500;
    text-decoration: underline;
  }
</style>
