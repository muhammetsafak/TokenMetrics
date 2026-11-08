<script lang="ts">
  // Brand mark (same geometry as assets/brand/mark-tile.svg) with an optional wordmark.
  interface Props {
    size?: number
    wordmark?: boolean
  }
  let { size = 32, wordmark = false }: Props = $props()
</script>

<span class="logo" style:--logo-size="{size}px">
  <svg
    class="mark"
    viewBox="0 0 64 64"
    width={size}
    height={size}
    role={wordmark ? undefined : 'img'}
    aria-label={wordmark ? undefined : 'TokenMetrics'}
    aria-hidden={wordmark ? 'true' : undefined}
    focusable="false"
  >
    <rect x="2" y="2" width="60" height="60" rx="14" fill="var(--brand-ink)" />
    <rect x="2.5" y="2.5" width="59" height="59" rx="13.5" fill="none" stroke="#ffffff" stroke-width="1" stroke-opacity="0.08" />
    <rect x="16" y="15" width="32" height="8" rx="4" fill="#f4f4f1" />
    <rect x="28" y="26.5" width="8" height="6.5" rx="2.6" fill="var(--brand-mark)" />
    <rect x="28" y="35.5" width="8" height="6.5" rx="2.6" fill="var(--brand-mark)" fill-opacity="0.62" />
    <rect x="28" y="44.5" width="8" height="6.5" rx="2.6" fill="var(--brand-mark)" fill-opacity="0.3" />
  </svg>
  {#if wordmark}<span class="word"><span class="light">Token</span><span class="bold">Metrics</span></span>{/if}
</span>

<style>
  .logo {
    display: inline-flex;
    align-items: center;
    gap: calc(var(--logo-size) * 0.3);
    vertical-align: middle;
  }
  .mark {
    flex: none;
    display: block;
  }
  .word {
    font-family: var(--font-display);
    font-size: calc(var(--logo-size) * 0.62);
    line-height: 1;
    letter-spacing: -0.01em;
    color: var(--text);
  }
  .light {
    font-weight: 400;
  }
  .bold {
    font-weight: 600;
  }
</style>
