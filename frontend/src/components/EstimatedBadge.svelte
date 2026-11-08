<script lang="ts">
  interface Props {
    /** estimate: computed from the price table; bound: a lower bound. */
    kind?: 'estimate' | 'bound'
    title?: string
    /** Overrides the default wording ("tahmini" / "alt sınır"), e.g. "yaklaşık". */
    text?: string
  }
  let { kind = 'estimate', title, text }: Props = $props()

  const label = $derived(text ?? (kind === 'bound' ? 'alt sınır' : 'tahmini'))
  const hint = $derived(
    title ??
      (kind === 'bound'
        ? 'Maliyet gerçekte daha yüksek olabilir'
        : 'Fiyat tablosundan hesaplanmış tahmini değer'),
  )
</script>

<span class="badge-est" title={hint}>{label}</span>

<style>
  .badge-est {
    vertical-align: middle;
    font-family: var(--font);
    font-weight: 500;
  }
</style>
