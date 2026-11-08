<script lang="ts">
  import { modelLabel, serviceLabel } from '../lib/format'
  import {
    customRangeValid,
    defaultFilter,
    filter,
    meta,
    RANGES,
    sourceLabels,
    type RangeKey,
  } from '../lib/stores'
  import MultiSelect from './MultiSelect.svelte'

  const serviceOptions = $derived(($meta?.services ?? []).map((v) => ({ value: v, label: serviceLabel(v) })))
  const modelOptions = $derived(($meta?.models ?? []).map((v) => ({ value: v, label: modelLabel(v) })))
  const sourceOptions = $derived(
    ($meta?.sources ?? [])
      .filter((s) => s.records > 0)
      .map((s) => ({ value: s.id, label: $sourceLabels[s.id] ?? s.id })),
  )

  const pristine = $derived(
    $filter.range === defaultFilter.range &&
      $filter.services.length === 0 &&
      $filter.models.length === 0 &&
      $filter.sources.length === 0,
  )
  const customInvalid = $derived($filter.range === 'custom' && !customRangeValid($filter))

  function setRange(id: RangeKey) {
    filter.update((f) => {
      const next = { ...f, range: id }
      if (id === 'custom' && !f.customFrom && !f.customTo) {
        const today = new Date()
        const iso = (d: Date) =>
          `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`
        next.customTo = iso(today)
        next.customFrom = iso(new Date(today.getFullYear(), today.getMonth(), today.getDate() - 6))
      }
      return next
    })
  }
</script>

<div class="bar" role="search" aria-label="Filtreler">
  <div class="seg" role="group" aria-label="Tarih aralığı">
    {#each RANGES as r (r.id)}
      <button
        type="button"
        aria-pressed={$filter.range === r.id}
        onclick={() => setRange(r.id)}>{r.label}</button
      >
    {/each}
  </div>

  {#if $filter.range === 'custom'}
    <div class="custom">
      <label>
        <span class="muted">Başlangıç</span>
        <input type="date" value={$filter.customFrom} onchange={(e) => filter.update((f) => ({ ...f, customFrom: e.currentTarget.value }))} />
      </label>
      <label>
        <span class="muted">Bitiş</span>
        <input type="date" value={$filter.customTo} onchange={(e) => filter.update((f) => ({ ...f, customTo: e.currentTarget.value }))} />
      </label>
      {#if customInvalid}
        <p class="warn" role="alert">Geçerli bir aralık seçin (başlangıç ≤ bitiş); şimdilik tüm veri gösteriliyor.</p>
      {/if}
    </div>
  {/if}

  <div class="dims">
    <MultiSelect
      label="Servis"
      options={serviceOptions}
      selected={$filter.services}
      onchange={(v) => filter.update((f) => ({ ...f, services: v }))}
    />
    <MultiSelect
      label="Model"
      options={modelOptions}
      selected={$filter.models}
      onchange={(v) => filter.update((f) => ({ ...f, models: v }))}
    />
    <MultiSelect
      label="Kaynak"
      options={sourceOptions}
      selected={$filter.sources}
      onchange={(v) => filter.update((f) => ({ ...f, sources: v }))}
    />
    {#if !pristine}
      <button type="button" class="btn reset" onclick={() => filter.set({ ...defaultFilter })}>Sıfırla</button>
    {/if}
  </div>
</div>

<style>
  .bar {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 10px 16px;
  }
  .custom {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
  }
  .custom label {
    display: flex;
    align-items: center;
    gap: 6px;
  }
  input[type='date'] {
    min-height: 40px;
    padding: 0 10px;
    border: 1px solid var(--border-strong);
    border-radius: var(--radius-sm);
    background: var(--surface);
    color: var(--text);
    color-scheme: light dark;
  }
  .warn {
    flex-basis: 100%;
    color: var(--critical-text);
    font-size: 0.85rem;
  }
  .dims {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px;
    min-width: 0;
  }
</style>
