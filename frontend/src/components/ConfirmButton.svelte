<script lang="ts">
  import { tick } from 'svelte'

  interface Props {
    label: string
    confirmLabel?: string
    /** Names the target for assistive technology, e.g. "Anthropic anahtarı". */
    target: string
    disabled?: boolean
    onconfirm: () => void
  }
  let { label, confirmLabel = 'Evet, sil', target, disabled = false, onconfirm }: Props = $props()

  let armed = $state(false)
  let trigger: HTMLButtonElement | undefined = $state()
  let cancelBtn: HTMLButtonElement | undefined = $state()
  let group: HTMLElement | undefined = $state()

  async function arm() {
    armed = true
    await tick()
    cancelBtn?.focus() // the safe choice gets the focus
  }

  async function disarm() {
    armed = false
    await tick()
    trigger?.focus()
  }

  function confirm() {
    // The button (and its row, possibly the whole table when it was the last
    // row) disappears: park the focus on the surrounding card, which stays,
    // instead of letting it fall back to the page start. Table regions are
    // deliberately skipped: they are replaced by an empty state.
    const host = group?.closest<HTMLElement>('section, li, [role="tabpanel"]')
    armed = false
    if (host) {
      if (!host.hasAttribute('tabindex')) host.tabIndex = -1
      host.focus({ preventScroll: true })
    }
    onconfirm()
  }
</script>

{#if armed}
  <span class="actions" role="group" aria-label="{target} silinsin mi?" bind:this={group}>
    <span class="ask">Emin misiniz?</span>
    <button type="button" class="btn btn-small btn-danger" onclick={confirm}>{confirmLabel}</button>
    <button type="button" class="btn btn-small" bind:this={cancelBtn} onclick={disarm}>Vazgeç</button>
  </span>
{:else}
  <button type="button" class="btn btn-small btn-danger" bind:this={trigger} {disabled} onclick={arm}>
    {label}<span class="sr-only"> — {target}</span>
  </button>
{/if}

<style>
  .ask {
    font-size: 13px;
    color: var(--text-2);
  }
  .btn-danger:hover:not(:disabled) {
    background: color-mix(in srgb, var(--critical) 10%, transparent);
  }
</style>
