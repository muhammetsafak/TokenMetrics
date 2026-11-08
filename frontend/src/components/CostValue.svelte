<script lang="ts">
  import type { Totals } from '../lib/api'
  import { costHint, costText } from '../lib/cost'
  import EstimatedBadge from './EstimatedBadge.svelte'

  interface Props {
    totals: Totals
  }
  let { totals }: Props = $props()

  const tierUncertain = $derived(totals.tierUncertainModels.length > 0)
  const hint = $derived(costHint(totals))
  const bound = $derived(totals.costPartial || tierUncertain)
  // The bound badge may stand for two reasons; the tooltip names each one that applies.
  const boundHint = $derived(
    [
      totals.costPartial ? hint : undefined,
      tierUncertain ? 'Uzun-prompt fiyat kademesi uygulanamadı; gerçek maliyet daha yüksek olabilir.' : undefined,
    ]
      .filter(Boolean)
      .join(' '),
  )
</script>

<span class="cost-text" class:na={totals.costUnavailable} title={hint}>{costText(totals)}</span>
{#if !totals.costUnavailable}
  {#if totals.costEstimated}<EstimatedBadge />{/if}
  {#if bound}<EstimatedBadge kind="bound" title={boundHint} />{/if}
{/if}

<style>
  .cost-text {
    font-family: var(--font-mono);
    font-variant-numeric: tabular-nums;
  }
  .na {
    font-family: var(--font);
    color: var(--text-2);
    font-style: italic;
  }
</style>
