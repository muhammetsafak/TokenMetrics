<script lang="ts">
  import { api, errorMessage, type SourceSetting } from '../../lib/api'
  import { formatAgo, formatInt } from '../../lib/format'
  import { createLoader, dataVersion, refreshMeta } from '../../lib/stores'

  const sources = createLoader<SourceSetting[]>()

  let busy = $state('') // id of the source an action is running for
  let error = $state('')
  let notice = $state('')

  const reload = () => sources.load(() => api.sourceSettings())

  $effect(() => {
    void $dataVersion
    reload()
  })

  /** Runs an action; a string it returns (or `done`) becomes the success notice. */
  async function run(id: string, fn: () => Promise<unknown>, done = '') {
    busy = id
    error = ''
    notice = ''
    try {
      const r = await fn()
      notice = typeof r === 'string' ? r : done
      await reload()
      void refreshMeta()
    } catch (e) {
      error = errorMessage(e)
    } finally {
      busy = ''
    }
  }

  const toggle = (s: SourceSetting, enabled: boolean) =>
    run(s.id, () => api.setSourceEnabled(s.id, enabled), `${s.label} ${enabled ? 'taramaya dahil edildi' : 'taramadan çıkarıldı'}.`)

  const pick = (s: SourceSetting) =>
    run(s.id, async () => {
      const canceled = await api.pickSourcePath(s.id)
      return canceled ? '' : `${s.label} klasörü güncellendi; sonraki taramada kullanılır.`
    })

  const resetPath = (s: SourceSetting) =>
    run(s.id, () => api.resetSourcePath(s.id), `${s.label} varsayılan klasöre döndü; sonraki taramada kullanılır.`)

  function statusText(s: SourceSetting): string {
    switch (s.lastStatus) {
      case 'done':
        return `Tamamlandı: ${formatInt(s.lastAdded)} yeni, ${formatInt(s.lastUpdated)} güncellenen kayıt, ${formatInt(s.lastFiles)} dosya`
      case 'skipped':
        return 'Atlandı'
      case 'error':
        return 'Hata'
      case 'running':
        return 'Taranıyor…'
      default:
        return ''
    }
  }
</script>

<div class="view">
  {#if $sources.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Kaynaklar yüklenemedi</h2>
      <p>{$sources.error}</p>
      <button type="button" class="btn" onclick={reload}>Yeniden dene</button>
    </section>
  {:else if !$sources.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <p class="muted intro">
      Kaynaklar her taramada sırayla okunur. Kapalı bir kaynak taranmaz; daha önce saklanmış kayıtları silinmez.
    </p>

    <div class="status" aria-live="polite">
      {#if error}
        <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{error}</span></p>
      {:else if notice}
        <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{notice}</span></p>
      {/if}
    </div>

    <ul class="list">
      {#each $sources.data as s (s.id)}
        <li class="card src">
          <div class="head">
            <h3>{s.label}</h3>
            <span class="pills">
              {#if s.collector}
                {#if s.active}
                  <span class="pill on">Etkin</span>
                {:else if !s.enabled}
                  <span class="pill">Kapalı</span>
                {:else}
                  <span class="pill warn">Pasif</span>
                {/if}
              {:else}
                <span class="pill">Taranmaz</span>
              {/if}
              <span class="pill">{formatInt(s.records)} kayıt</span>
            </span>
            {#if s.collector}
              <label class="check switch">
                <input
                  type="checkbox"
                  role="switch"
                  checked={s.enabled}
                  disabled={busy === s.id}
                  onchange={(e) => toggle(s, e.currentTarget.checked)}
                />
                Taramaya dahil et
              </label>
            {/if}
          </div>

          {#if s.kind === 'api' && s.enabled && !s.hasKey}
            <p class="callout warn">
              <span aria-hidden="true">▲</span>
              <span class="body">Admin key eklenmediği için bu kaynak taranmıyor. "API Key'leri" sekmesinden ekleyin.</span>
            </p>
          {:else if s.id === 'codex_cli'}
            <p class="muted note">
              Güncel Codex CLI sürümleri günlüğe yalnız toplam token yazar (girdi/çıktı ayrımı yok). Bu kayıtlar toplam
              token'a girer; maliyetleri hesaplanamadığı için "hesaplanamadı" görünür.
            </p>
          {:else if s.kind === 'manual'}
            <p class="muted">Bu kaynak taranmaz; kayıtlar Veri ekranından eklenir.</p>
          {/if}

          {#if s.pathEditable}
            <div class="path">
              <p class="label">Günlük klasörü {s.pathOverride ? '(özel)' : '(varsayılan)'}</p>
              <p class="mono val">{s.effectivePath || '—'}</p>
              {#if s.effectivePath && !s.pathExists}
                <p class="callout warn">
                  <span aria-hidden="true">▲</span>
                  <span class="body">Klasör bulunamadı; araç kurulu değilse ya da farklı bir yere yazıyorsa bu normaldir.</span>
                </p>
              {/if}
              <div class="actions">
                <button type="button" class="btn btn-small" disabled={busy === s.id} onclick={() => pick(s)}>Klasör seç…</button>
                {#if s.pathOverride}
                  <button type="button" class="btn btn-small" disabled={busy === s.id} onclick={() => resetPath(s)}>Varsayılana dön</button>
                {/if}
              </div>
            </div>
          {/if}

          {#if s.lastStatus}
            <div class="last">
              <p>
                <span class="label">Son tarama:</span>
                {statusText(s)}
                {#if s.lastScanMs}<span class="muted">({formatAgo(s.lastScanMs)})</span>{/if}
              </p>
              {#if s.lastError}
                <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{s.lastError}</span></p>
              {/if}
              {#if s.lastWarnings.length > 0}
                <details>
                  <summary>{s.lastWarnings.length} uyarı</summary>
                  <ul class="warnings">
                    {#each s.lastWarnings as w, i (i)}<li>{w}</li>{/each}
                  </ul>
                </details>
              {/if}
            </div>
          {/if}
        </li>
      {/each}
    </ul>
  {/if}
</div>

<style>
  .list {
    display: grid;
    gap: 12px;
    margin: 0;
    padding: 0;
    list-style: none;
  }
  .src {
    display: grid;
    gap: 12px;
  }
  .head {
    display: flex;
    flex-wrap: wrap;
    align-items: center;
    gap: 8px 12px;
  }
  .head h3 {
    font-family: var(--font-display);
    font-size: 16px;
    font-weight: 600;
  }
  .pills {
    display: inline-flex;
    flex-wrap: wrap;
    gap: 6px;
  }
  .switch {
    margin-left: auto;
  }
  .label {
    font-size: 12px;
    font-weight: 500;
    color: var(--text-3);
  }
  .path {
    display: grid;
    gap: 6px;
  }
  .val {
    overflow-wrap: anywhere;
  }
  .last {
    display: grid;
    gap: 6px;
    font-size: 0.88rem;
  }
  .warnings {
    margin: 6px 0 0;
    padding-left: 18px;
    overflow-wrap: anywhere;
    font-size: 0.85rem;
  }
  summary {
    cursor: pointer;
  }
  .intro {
    font-size: 0.9rem;
  }
</style>
