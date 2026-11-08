<script lang="ts">
  import { onMount } from 'svelte'
  import { formatAgo, localTimeZone } from './lib/format'
  import {
    initApp,
    meta,
    metaError,
    scan,
    scanIssues,
    startScan,
    view,
    VIEWS,
  } from './lib/stores'
  import FilterBar from './components/FilterBar.svelte'
  import Logo from './components/Logo.svelte'
  import Compare from './views/Compare.svelte'
  import Data from './views/Data.svelte'
  import Limits from './views/Limits.svelte'
  import Overview from './views/Overview.svelte'
  import Projects from './views/Projects.svelte'
  import Settings from './views/Settings.svelte'

  let now = $state(Date.now())
  const timeZone = localTimeZone()

  onMount(() => {
    const stop = initApp()
    const tick = setInterval(() => (now = Date.now()), 30_000)
    return () => {
      stop()
      clearInterval(tick)
    }
  })

  const lastScanMs = $derived($scan.last?.finishedMs || $meta?.lastScanMs || 0)
  const warningLines = $derived(
    ($scan.last?.steps ?? []).flatMap((s) => s.warnings.map((w) => `${s.label}: ${w}`)),
  )
</script>

<div class="shell">
  <header class="top">
    <div class="brand">
      <h1><Logo size={36} wordmark /></h1>
      <p class="status" class:scanning={$scan.scanning} aria-live="polite">
        <span class="dot" aria-hidden="true"></span>
        <span>
          {#if $scan.scanning}
            Taranıyor…
            {#if $scan.step}{$scan.step.label} ({$scan.step.index}/{$scan.step.total}){/if}
          {:else if lastScanMs > 0}
            Son tarama: {formatAgo(lastScanMs, now)}
          {:else}
            Henüz taranmadı
          {/if}
        </span>
      </p>
    </div>
    <nav aria-label="Ana gezinme">
      {#each VIEWS as v (v.id)}
        <button type="button" aria-current={$view === v.id ? 'page' : undefined} onclick={() => view.set(v.id)}>{v.label}</button>
      {/each}
    </nav>
    <button type="button" class="btn btn-primary scan" class:spinning={$scan.scanning} disabled={$scan.scanning} onclick={startScan}>
      <svg class="refresh" viewBox="0 0 24 24" width="16" height="16" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
        <path d="M20 11a8 8 0 0 0-14.4-4.4L4 8.5" />
        <path d="M4 4v4.5h4.5" />
        <path d="M4 13a8 8 0 0 0 14.4 4.4L20 15.5" />
        <path d="M20 20v-4.5h-4.5" />
      </svg>
      {$scan.scanning ? 'Taranıyor…' : 'Yeniden tara'}
    </button>
  </header>

  {#if $metaError}
    <section class="card state error banner alert" role="alert">
      {@render warnIcon()}
      <div class="msg">
        <h2>Uygulama servisi çalışmıyor</h2>
        <p>{$metaError}</p>
      </div>
    </section>
  {/if}

  {#if $scanIssues.errors.length > 0}
    <div class="issue alert" role="alert">
      {@render warnIcon()}
      <div class="msg">
        <strong>Bazı kaynaklar taranamadı:</strong>
        <ul>
          {#each $scanIssues.errors as s (s.source)}
            <li>{s.label}: {s.error}</li>
          {/each}
        </ul>
      </div>
    </div>
  {/if}
  {#if warningLines.length > 0}
    <details class="issue">
      <summary>
        {@render warnIcon()}
        <span>{warningLines.length} tarama uyarısı</span>
        <svg class="chevron" viewBox="0 0 24 24" width="14" height="14" fill="none" stroke="currentColor" stroke-width="2.2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
          <path d="m6 9 6 6 6-6" />
        </svg>
      </summary>
      <ul>
        {#each warningLines as w, i (i)}
          <li>{w}</li>
        {/each}
      </ul>
    </details>
  {/if}

  {#if $view === 'overview' || $view === 'compare' || $view === 'projects'}
    <div class="filters card">
      <FilterBar />
    </div>
  {/if}

  <main id="main" tabindex="-1">
    {#if $view === 'overview'}
      <Overview />
    {:else if $view === 'compare'}
      <Compare />
    {:else if $view === 'projects'}
      <Projects />
    {:else if $view === 'data'}
      <Data />
    {:else if $view === 'settings'}
      <Settings />
    {:else}
      <Limits />
    {/if}
  </main>

  <footer class="muted">
    <p>
      Maliyet değerleri tahminidir{#if $meta?.priceAsOf} (fiyat tablosu tarihi: {$meta.priceAsOf}){/if}. Saatler yerel saat
      diliminizde ({timeZone}) gösterilir.
    </p>
  </footer>
</div>

{#snippet warnIcon()}
  <svg class="warn-icon" viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true" focusable="false">
    <path d="M10.3 3.9 2.4 17.5A2 2 0 0 0 4.1 20.5h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0Z" />
    <path d="M12 9.5v4" />
    <path d="M12 17h.01" />
  </svg>
{/snippet}

<style>
  .shell {
    display: grid;
    gap: 16px;
    max-width: 1200px;
    margin: 0 auto;
    padding: 20px var(--gutter) 32px;
  }
  .top {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 12px 20px;
  }
  .brand {
    display: grid;
    gap: 6px;
    margin-right: auto;
    min-width: 0;
  }
  h1 {
    font-size: 1rem;
    line-height: 1;
  }
  .status {
    display: flex;
    align-items: center;
    gap: 8px;
    color: var(--text-2);
    font-size: 0.82rem;
  }
  .dot {
    flex: none;
    width: 6px;
    height: 6px;
    border-radius: 50%;
    background: var(--status-ok);
  }
  .status.scanning .dot {
    background: var(--accent);
  }
  nav {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
    padding: 4px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
  }
  nav button {
    min-height: 36px;
    padding: 0 14px;
    border: 0;
    border-radius: 8px;
    background: none;
    color: var(--text-2);
    font-weight: 500;
  }
  nav button:hover {
    background: var(--hover);
  }
  nav button[aria-current='page'] {
    background: var(--selected);
    color: var(--on-selected);
    font-weight: 600;
  }
  .scan {
    min-height: 44px;
    padding: 0 18px;
  }
  .refresh {
    flex: none;
  }
  .filters {
    padding: 14px 16px;
  }

  /* Warning band: amber frame and wash; the alert variant uses the critical color. */
  .issue {
    --tone: var(--warning);
    padding: 12px 16px;
    border: 1px solid color-mix(in srgb, var(--tone) 35%, transparent);
    border-radius: var(--radius-sm);
    background: color-mix(in srgb, var(--tone) 8%, var(--surface));
    font-size: 0.88rem;
    min-width: 0;
  }
  .issue.alert {
    --tone: var(--critical);
  }
  .issue,
  .banner {
    display: flex;
    gap: 10px;
    align-items: flex-start;
  }
  details.issue {
    display: block;
  }
  .issue .warn-icon,
  .banner .warn-icon {
    flex: none;
    margin-top: 1px;
    color: var(--tone);
  }
  .banner {
    --tone: var(--critical);
    padding: 14px 16px;
    border-color: color-mix(in srgb, var(--tone) 35%, transparent);
    background: color-mix(in srgb, var(--tone) 8%, var(--surface));
    border-radius: var(--radius-sm);
  }
  .banner .msg h2 {
    font-size: 1rem;
  }
  .msg {
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .issue ul {
    margin: 6px 0 0;
    padding-left: 18px;
    overflow-wrap: anywhere;
  }
  .issue summary {
    display: flex;
    align-items: center;
    gap: 10px;
    cursor: pointer;
    font-weight: 500;
  }
  .issue summary .warn-icon {
    margin-top: 0;
  }
  /* summary is display:flex, so the native marker is gone; the chevron shows open/closed. */
  .issue summary::-webkit-details-marker {
    display: none;
  }
  .issue summary .chevron {
    flex: none;
    margin-left: auto;
    color: var(--text-2);
  }
  details[open] > summary .chevron {
    transform: rotate(180deg);
  }
  main {
    min-width: 0;
  }
  main:focus {
    outline: none;
  }
  footer {
    font-size: 0.8rem;
  }

  @media (prefers-reduced-motion: no-preference) {
    .status.scanning .dot {
      animation: pulse 1.2s ease-in-out infinite;
    }
    .scan.spinning .refresh {
      animation: spin 1s linear infinite;
    }
  }
  @keyframes pulse {
    0%,
    100% {
      opacity: 1;
      transform: scale(1);
    }
    50% {
      opacity: 0.35;
      transform: scale(1.5);
    }
  }
  @keyframes spin {
    to {
      transform: rotate(360deg);
    }
  }
</style>
