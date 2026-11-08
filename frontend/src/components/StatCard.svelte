<script lang="ts">
  import type { Totals } from '../lib/api'
  import { formatInt, formatTokens } from '../lib/format'
  import CostValue from './CostValue.svelte'

  interface Props {
    label: string
    totals: Totals
  }
  let { label, totals }: Props = $props()
</script>

<section class="card stat" aria-label={label}>
  <h3>{label}</h3>
  <p class="value" title="{formatInt(totals.totalTokens)} token">
    {formatTokens(totals.totalTokens)}<span class="unit"> token</span>
  </p>
  <p class="cost">
    <CostValue {totals} />
  </p>
  <p class="meta">{formatInt(totals.requests)} istek · {formatInt(totals.records)} kayıt</p>
  {#if totals.totalOnlyTokens > 0}
    <p class="meta" title="Girdi/çıktı ayrımı olmayan, yalnız toplam token yazan kayıtlar">
      {formatTokens(totals.totalOnlyTokens)} token bölünmemiş (yalnız toplam)
    </p>
  {/if}
</section>

<style>
  .stat h3 {
    font-size: 13px;
    font-weight: 500;
    color: var(--text-2);
  }
  .value {
    margin-top: 8px;
    font-family: var(--font-display);
    font-size: 40px;
    font-weight: 600;
    letter-spacing: -0.02em;
    line-height: 1.1;
    overflow-wrap: anywhere;
  }
  .unit {
    margin-left: 2px;
    font-family: var(--font);
    font-size: 16px;
    font-weight: 400;
    letter-spacing: 0;
    color: var(--text-2);
  }
  .cost {
    margin-top: 10px;
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 6px;
  }
  .cost :global(.cost-text) {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
    font-size: 15px;
    font-weight: 500;
  }
  .meta {
    margin-top: 4px;
    font-size: 12px;
    color: var(--text-3);
  }
</style>
