<script lang="ts">
  import { api, type LimitItem, type LimitsData } from '../lib/api'
  import {
    formatDateTime,
    formatInt,
    formatTokens,
    LIMIT_KIND_LABELS,
    METRIC_LABELS,
    serviceLabel,
  } from '../lib/format'
  import {
    limitLevel as level,
    limitPercentText as percentText,
    limitUsedText as usedText,
    limitValue as value,
  } from '../lib/limits'
  import { createLoader, dataVersion } from '../lib/stores'
  import CostNotes from '../components/CostNotes.svelte'
  import CostValue from '../components/CostValue.svelte'
  import EstimatedBadge from '../components/EstimatedBadge.svelte'

  const limits = createLoader<LimitsData>()

  $effect(() => {
    void $dataVersion
    limits.load(() => api.limits())
  })

  const ICONS = { ok: '●', near: '▲', over: '■', unk: '○' }

  const title = (l: LimitItem) =>
    `${l.service ? serviceLabel(l.service) : 'Tüm servisler'} — ${LIMIT_KIND_LABELS[l.kind] ?? l.kind}`

  const uncertain = $derived([...new Set(($limits.data?.limits ?? []).flatMap((l) => l.tierUncertainModels))])
  const unknown = $derived([...new Set(($limits.data?.limits ?? []).flatMap((l) => l.unknownModels))])
  const currentTotals = $derived(($limits.data?.current ?? []).map((c) => c.totals))
</script>

<div class="view">
  {#if $limits.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Limitler yüklenemedi</h2>
      <p>{$limits.error}</p>
      <button type="button" class="btn" onclick={() => limits.load(() => api.limits())}>Yeniden dene</button>
    </section>
  {:else if !$limits.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <section class="card" aria-label="Limit pencereleri">
      <div class="section-head">
        <h2>Limit pencereleri <EstimatedBadge title="Abonelik limitleri resmî olarak yayınlanmaz; değerler tahminidir" /></h2>
      </div>
      {#if $limits.data.limits.length === 0}
        <div class="state" aria-live="polite">
          <h2>Limit tanımlı değil</h2>
          <p>Limit değerleri Ayarlar'dan girilir. Tanımlanana kadar aşağıda mevcut pencerelerdeki tüketim gösterilir.</p>
        </div>
      {:else}
        <ul class="bars">
          {#each $limits.data.limits as l (`${l.service}|${l.kind}`)}
            {@const lower = l.missingTotalOnlyTokens > 0}
            {@const lv = level(l.ratio, lower)}
            <li>
              <div class="row">
                <h3>{title(l)} <EstimatedBadge /></h3>
                <p class="nums">
                  <span class="num"><strong>{usedText(l)}</strong> / {value(l.metric, l.limit)}</span>
                  <span class="muted">({METRIC_LABELS[l.metric] ?? l.metric})</span>
                  {#if lower}<EstimatedBadge kind="bound" title="Yalnız toplam token yazan kayıtlar bu ölçüt için sayılamadı; gerçek kullanım daha yüksek." />{/if}
                </p>
              </div>
              <div
                class="track"
                role="progressbar"
                aria-label={title(l)}
                aria-valuemin="0"
                aria-valuemax="100"
                aria-valuenow={Math.min(100, Math.round(l.ratio * 100))}
                aria-valuetext="{percentText(l)}, {lv.text}"
              >
                <div class="fill {lv.id}" style:width="{Math.min(100, l.ratio * 100)}%"></div>
              </div>
              <p class="foot">
                <span class="status {lv.id}"><span class="icon" aria-hidden="true">{ICONS[lv.id]}</span> {percentText(l)} · {lv.text}</span>
                <span class="muted">
                  {#if l.kind === 'weekly'}Sıfırlanma: {formatDateTime(l.windowEndMs)}{:else}Pencere: son 5 saat (kayan){/if}
                </span>
              </p>
              {#if l.missingTotalOnlyTokens > 0}
                <p class="muted small" role="note">
                  Alt sınır: yalnız toplam token yazan kayıtların {formatInt(l.missingTotalOnlyTokens)} token'ı bu ölçüt için sayılamadı;
                  gerçek kullanım daha yüksek.
                </p>
              {/if}
            </li>
          {/each}
        </ul>
      {/if}
    </section>

    <section class="card" aria-label="Mevcut pencereler">
      <div class="section-head"><h2>Mevcut pencerelerdeki tüketim</h2></div>
      <div class="current">
        {#each $limits.data.current as c (c.kind)}
          <div class="win">
            <h3>{LIMIT_KIND_LABELS[c.kind] ?? c.kind}</h3>
            <p class="big" title="{formatInt(c.totals.totalTokens)} token">{formatTokens(c.totals.totalTokens)}<span class="unit"> token</span></p>
            <p><CostValue totals={c.totals} /></p>
            {#if c.totals.totalOnlyTokens > 0}
              <p class="muted small">{formatTokens(c.totals.totalOnlyTokens)} token bölünmemiş (yalnız toplam)</p>
            {/if}
            <p class="muted small">
              {formatDateTime(c.startMs)} → {c.kind === 'weekly' ? `sıfırlanma ${formatDateTime(c.endMs)}` : 'şimdi'}
            </p>
          </div>
        {/each}
      </div>
    </section>

    <CostNotes
      totals={currentTotals}
      {uncertain}
      {unknown}
      asOf={$limits.data.priceAsOf}
      extra="Limit değerleri ve yüzdeler tahminidir. 5 saatlik pencere burada son 5 saat olarak (kayan) hesaplanır; Claude aboneliklerinde 5 saatlik blok ilk mesajla başlayan sabit bir pencere olabilir, bu yüzden gerçek sıfırlanma zamanı farklı olabilir."
    />
  {/if}
</div>

<style>
  .bars {
    display: grid;
    gap: 22px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .row {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    align-items: baseline;
    gap: 4px 12px;
  }
  h3 {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-2);
  }
  .nums {
    font-size: 13px;
  }
  .nums strong {
    font-weight: 500;
  }
  .track {
    height: 8px;
    margin: 8px 0 6px;
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
  .foot {
    display: flex;
    flex-wrap: wrap;
    justify-content: space-between;
    gap: 2px 12px;
    font-size: 12px;
  }
  .status {
    font-weight: 600;
  }
  .status.over {
    color: var(--critical-text);
  }
  .status.unk {
    color: var(--text-2);
  }
  .icon {
    font-size: 0.7rem;
  }
  .status.ok .icon {
    color: var(--good);
  }
  .status.near .icon {
    color: var(--warning);
  }
  .status.over .icon {
    color: var(--critical);
  }
  .current {
    display: grid;
    grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
    gap: 12px;
  }
  .win {
    padding: 16px;
    background: var(--surface-2);
    border-radius: var(--radius-sm);
    display: grid;
    gap: 4px;
  }
  .big {
    font-family: var(--font-display);
    font-size: 28px;
    font-weight: 600;
    letter-spacing: -0.02em;
    line-height: 1.15;
    overflow-wrap: anywhere;
  }
  .unit {
    margin-left: 4px;
    font-family: var(--font);
    font-size: 14px;
    font-weight: 400;
    letter-spacing: 0;
    color: var(--text-3);
  }
  .small {
    font-size: 12px;
  }
</style>
