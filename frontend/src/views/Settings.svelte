<script lang="ts">
  import '../lib/forms.css'
  import Sources from './settings/Sources.svelte'
  import ApiKeys from './settings/ApiKeys.svelte'
  import Prices from './settings/Prices.svelte'
  import Limits from './settings/Limits.svelte'
  import Budgets from './settings/Budgets.svelte'

  const TABS = [
    { id: 'sources', label: 'Kaynaklar' },
    { id: 'keys', label: "API Key'leri" },
    { id: 'prices', label: 'Fiyatlar' },
    { id: 'limits', label: 'Limitler' },
    { id: 'budgets', label: 'Bütçeler' },
  ] as const
  type TabId = (typeof TABS)[number]['id']

  let active = $state<TabId>('sources')
  const tabEls: Record<string, HTMLButtonElement | undefined> = {}

  function onKey(e: KeyboardEvent, i: number) {
    let next = -1
    if (e.key === 'ArrowRight') next = (i + 1) % TABS.length
    else if (e.key === 'ArrowLeft') next = (i - 1 + TABS.length) % TABS.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = TABS.length - 1
    if (next < 0) return
    e.preventDefault()
    active = TABS[next].id
    tabEls[active]?.focus()
  }
</script>

<div class="view">
  <h2 class="sr-only">Ayarlar</h2>
  <div class="tabs" role="tablist" aria-label="Ayar bölümleri">
    {#each TABS as t, i (t.id)}
      <button
        type="button"
        role="tab"
        id="tab-{t.id}"
        aria-selected={active === t.id}
        aria-controls="panel-{t.id}"
        tabindex={active === t.id ? 0 : -1}
        bind:this={tabEls[t.id]}
        onclick={() => (active = t.id)}
        onkeydown={(e) => onKey(e, i)}>{t.label}</button
      >
    {/each}
  </div>

  <div role="tabpanel" id="panel-{active}" aria-labelledby="tab-{active}" class="panel">
    {#if active === 'sources'}
      <Sources />
    {:else if active === 'keys'}
      <ApiKeys />
    {:else if active === 'prices'}
      <Prices />
    {:else if active === 'limits'}
      <Limits />
    {:else}
      <Budgets />
    {/if}
  </div>
</div>

<style>
  .tabs {
    display: flex;
    gap: 4px;
    overflow-x: auto;
    overflow-y: hidden;
    border-bottom: 1px solid var(--border);
  }
  .tabs button {
    flex: none;
    min-height: 40px;
    padding: 0 14px;
    border: 0;
    border-bottom: 2px solid transparent;
    background: none;
    color: var(--text-2);
    font-size: 14px;
    font-weight: 500;
  }
  .tabs button:hover {
    background: var(--hover);
    color: var(--text);
    border-radius: var(--radius-sm) var(--radius-sm) 0 0;
  }
  /* The scrolling tab strip would clip an outset outline. */
  .tabs button:focus-visible {
    outline-offset: -2px;
  }
  .tabs button[aria-selected='true'] {
    color: var(--text);
    font-weight: 600;
    border-bottom-color: var(--accent);
  }
  .panel {
    display: grid;
    gap: 16px;
    min-width: 0;
  }
</style>
