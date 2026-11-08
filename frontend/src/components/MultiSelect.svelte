<script lang="ts">
  interface Option {
    value: string
    label: string
  }
  interface Props {
    label: string
    options: Option[]
    selected: string[]
    onchange: (values: string[]) => void
  }
  let { label, options, selected, onchange }: Props = $props()

  let open = $state(false)
  let root: HTMLDivElement
  let button: HTMLButtonElement
  const uid = $props.id()

  const summary = $derived.by(() => {
    if (selected.length === 0) return 'Tümü'
    if (selected.length === 1) return options.find((o) => o.value === selected[0])?.label ?? selected[0]
    return `${selected.length} seçili`
  })

  function toggle(value: string, checked: boolean) {
    onchange(checked ? [...selected, value] : selected.filter((v) => v !== value))
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === 'Escape' && open) {
      open = false
      button.focus()
    }
  }

  function onWindowPointer(e: Event) {
    if (open && !root.contains(e.target as Node)) open = false
  }
</script>

<svelte:window onpointerdown={onWindowPointer} />

<div class="ms" bind:this={root} onkeydown={onKeydown} role="presentation">
  <button
    type="button"
    class="trigger"
    bind:this={button}
    aria-expanded={open}
    aria-controls="{uid}-panel"
    disabled={options.length === 0}
    onclick={() => (open = !open)}
  >
    <span class="lbl">{label}</span>
    <span class="val">{summary}</span>
    <svg class="caret" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
      <path d="m6 9 6 6 6-6" />
    </svg>
  </button>
  {#if open}
    <div class="panel" id="{uid}-panel" role="group" aria-label={label}>
      {#each options as o (o.value)}
        <label class="opt">
          <input
            type="checkbox"
            checked={selected.includes(o.value)}
            onchange={(e) => toggle(o.value, e.currentTarget.checked)}
          />
          <span>{o.label}</span>
        </label>
      {/each}
      {#if selected.length > 0}
        <button type="button" class="clear" onclick={() => onchange([])}>Seçimi temizle</button>
      {/if}
    </div>
  {/if}
</div>

<style>
  .ms {
    position: relative;
    min-width: 0;
  }
  .trigger {
    display: flex;
    align-items: center;
    gap: 6px;
    max-width: 100%;
    min-height: 40px;
    padding: 0 12px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface);
  }
  .trigger:disabled {
    opacity: 0.55;
    cursor: default;
  }
  .lbl {
    color: var(--text-2);
  }
  .val {
    color: var(--text);
    font-weight: 600;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    max-width: 14ch;
  }
  .caret {
    flex: none;
    color: var(--text-3);
  }
  .trigger[aria-expanded='true'] .caret {
    transform: rotate(180deg);
  }
  .panel {
    position: absolute;
    z-index: 20;
    top: calc(100% + 4px);
    left: 0;
    min-width: 200px;
    max-width: min(320px, calc(100vw - 32px));
    max-height: 320px;
    overflow: auto;
    padding: 6px;
    background: var(--surface);
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    box-shadow: 0 6px 20px rgba(0, 0, 0, 0.18);
  }
  .opt {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 6px 8px;
    border-radius: 8px;
    cursor: pointer;
    overflow-wrap: anywhere;
  }
  .opt:hover {
    background: var(--hover);
  }
  .opt input {
    accent-color: var(--accent);
    width: 16px;
    height: 16px;
    flex: none;
  }
  .clear {
    width: 100%;
    margin-top: 4px;
    padding: 6px 8px;
    border: 0;
    border-top: 1px solid var(--border);
    background: none;
    color: var(--accent);
    text-align: left;
  }
</style>
