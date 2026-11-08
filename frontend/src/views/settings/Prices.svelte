<script lang="ts">
  import { tick } from 'svelte'
  import { api, errorMessage, type PriceOverride, type PriceRow, type PricesData } from '../../lib/api'
  import { formatInt, serviceLabel } from '../../lib/format'
  import { isEmptyNumber } from '../../lib/input'
  import { createLoader, dataVersion } from '../../lib/stores'
  import DataTable, { type Column } from '../../components/DataTable.svelte'

  const prices = createLoader<PricesData>()
  const reload = () => prices.load(() => api.prices())
  $effect(() => {
    reload()
  })

  const usd = new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 4 })
  const price = (v: number | undefined) => (v === undefined ? '—' : usd.format(v))

  let query = $state('')
  const rows = $derived(
    ($prices.data?.rows ?? []).filter((r) => {
      const q = query.trim().toLowerCase()
      return q === '' || r.model.toLowerCase().includes(q) || r.service.toLowerCase().includes(q)
    }),
  )

  function stateOf(r: PriceRow): string {
    if (r.custom) return r.effective ? 'Özel model' : 'Özel (fiyatsız)'
    if (r.overridden) return 'Özelleştirilmiş'
    if (r.expired) return 'Süresi doldu'
    return 'Varsayılan'
  }

  const columns: Column<PriceRow>[] = [
    { key: 'model', label: 'Model', value: (r) => r.model, primary: true },
    { key: 'service', label: 'Servis', value: (r) => (r.custom ? '' : r.service), text: (r) => (r.custom ? 'özel' : serviceLabel(r.service)) },
    { key: 'input', label: 'Giriş', align: 'right', value: (r) => r.effective?.input ?? null, text: (r) => price(r.effective?.input) },
    { key: 'output', label: 'Çıktı', align: 'right', value: (r) => r.effective?.output ?? null, text: (r) => price(r.effective?.output) },
    { key: 'cacheRead', label: 'Önbellek okuma', align: 'right', value: (r) => r.effective?.cacheRead ?? null, text: (r) => price(r.effective?.cacheRead) },
    { key: 'cacheWrite5m', label: 'Önb. yazma 5 dk', align: 'right', value: (r) => r.effective?.cacheWrite5m ?? null, text: (r) => price(r.effective?.cacheWrite5m) },
    { key: 'cacheWrite1h', label: 'Önb. yazma 1 sa', align: 'right', value: (r) => r.effective?.cacheWrite1h ?? null, text: (r) => price(r.effective?.cacheWrite1h) },
    { key: 'state', label: 'Durum', value: (r) => stateOf(r) },
  ]

  // ---- editor ----

  interface Draft {
    /** The model being edited; null while adding a custom one. */
    row: PriceRow | null
    model: string
    input: number | null
    output: number | null
    cacheRead: number | null
    cacheWrite5m: number | null
    cacheWrite1h: number | null
  }

  let draft = $state<Draft | null>(null)
  let busy = $state(false)
  let error = $state('')
  let notice = $state('')
  let editor: HTMLElement | undefined = $state()

  const nul = (v: number | undefined) => (v === undefined ? null : v)
  const undef = (v: number | null) => (isEmptyNumber(v) ? undefined : v)

  async function open(row: PriceRow | null) {
    error = ''
    notice = ''
    const o = row?.override
    draft = {
      row,
      model: row?.model ?? '',
      input: nul(o?.input),
      output: nul(o?.output),
      cacheRead: nul(o?.cacheRead),
      cacheWrite5m: nul(o?.cacheWrite5m),
      cacheWrite1h: nul(o?.cacheWrite1h),
    }
    await tick()
    editor?.scrollIntoView({ block: 'nearest' })
    editor?.querySelector<HTMLInputElement>('input')?.focus()
  }

  function close() {
    draft = null
    error = ''
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    if (!draft) return
    const d = draft
    const o: PriceOverride = {
      model: d.model.trim(),
      input: undef(d.input),
      output: undef(d.output),
      cacheRead: undef(d.cacheRead),
      cacheWrite5m: undef(d.cacheWrite5m),
      cacheWrite1h: undef(d.cacheWrite1h),
    }
    if (!o.model) {
      error = 'Model adı girilmeli.'
      return
    }
    if (!d.row && (o.input === undefined || o.output === undefined)) {
      error = 'Listede olmayan bir model için giriş ve çıktı fiyatı birlikte girilmeli; yoksa model fiyatsız kalır.'
      return
    }
    busy = true
    error = ''
    try {
      await api.setPriceOverride(o)
      notice = `${o.model} için özel fiyat kaydedildi; maliyetler yeni fiyatla hesaplanır.`
      draft = null
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }

  async function reset(model: string) {
    busy = true
    error = ''
    try {
      await api.resetPriceOverride(model)
      notice = `${model} için özel fiyat kaldırıldı.`
      draft = null
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }

  const FIELDS = [
    { key: 'input', label: 'Giriş' },
    { key: 'output', label: 'Çıktı' },
    { key: 'cacheRead', label: 'Önbellek okuma' },
    { key: 'cacheWrite5m', label: 'Önbellek yazma (5 dk)' },
    { key: 'cacheWrite1h', label: 'Önbellek yazma (1 sa)' },
  ] as const
</script>

<div class="view">
  {#if $prices.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Fiyat tablosu yüklenemedi</h2>
      <p>{$prices.error}</p>
      <button type="button" class="btn" onclick={reload}>Yeniden dene</button>
    </section>
  {:else if !$prices.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <section class="card" aria-label="Fiyat tablosu">
      <div class="section-head">
        <h2>Model fiyatları <span class="muted unit">(USD / 1 milyon token)</span></h2>
        <button type="button" class="btn btn-small" onclick={() => open(null)}>Özel model ekle</button>
      </div>
      <p class="muted note">
        Fiyatlar uygulamayla gelen tablodan okunur (tablo tarihi: <time datetime={$prices.data.asOf}>{$prices.data.asOf}</time>) ve
        tahmini maliyet hesabında kullanılır. Bir fiyat yanlışsa ya da değiştiyse model adına tıklayıp kendi değerinizi
        girebilirsiniz; girmediğiniz alanlar varsayılan fiyatı korur.
      </p>

      <div aria-live="polite">
        {#if notice}
          <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{notice}</span></p>
        {/if}
      </div>

      <div class="field search">
        <label for="price-q">Model ya da servis ara</label>
        <input id="price-q" type="search" autocomplete="off" bind:value={query} />
      </div>

      {#if rows.length === 0}
        <div class="state" aria-live="polite">
          <h2>Eşleşen model yok</h2>
          <p>Aramayı değiştirin ya da "Özel model ekle" ile yeni bir model tanımlayın.</p>
        </div>
      {:else}
        <DataTable
          {columns}
          {rows}
          rowKey={(r) => r.model}
          caption="Model fiyat tablosu"
          onRowClick={(r) => open(r)}
          selectedKey={draft?.row?.model ?? null}
          defaultSort={{ key: 'model', dir: 'asc' }}
        />
        <p class="muted small">{formatInt(rows.length)} model gösteriliyor. Düzenlemek için model adına tıklayın.</p>
      {/if}
    </section>

    {#if draft}
      {@const d = draft}
      <section class="card" aria-label="Fiyat düzenleme" bind:this={editor}>
        <div class="section-head">
          <h2>{d.row ? `${d.row.model} fiyatı` : 'Özel model ekle'}</h2>
        </div>
        <form onsubmit={save}>
          {#if !d.row}
            <div class="field">
              <label for="p-model">Model adı</label>
              <input id="p-model" type="text" maxlength="256" autocomplete="off" required bind:value={d.model} />
              <span class="hint">Kayıtlardaki model adıyla aynı olmalı; büyük/küçük harf fark etmez.</span>
            </div>
          {/if}
          <div class="fields">
            {#each FIELDS as f (f.key)}
              {@const def = d.row?.default?.[f.key]}
              <div class="field">
                <label for="p-{f.key}">{f.label}</label>
                <input
                  id="p-{f.key}"
                  type="number"
                  min="0"
                  max="1000000"
                  step="any"
                  inputmode="decimal"
                  placeholder={def === undefined ? '' : `varsayılan ${price(def)}`}
                  bind:value={d[f.key]}
                />
              </div>
            {/each}
          </div>
          <p class="muted small">
            Boş bıraktığınız alan varsayılan fiyatı kullanır.
            {#if d.row?.longPrompt}
              Bu modelin {formatInt($prices.data.longPromptThreshold)} token üzerindeki istemleri için ayrı bir fiyatı var;
              girdiğiniz değerler her iki kademeye de uygulanır.
            {/if}
            {#if d.row?.expired}
              Bu fiyatın geçerlilik dönemi {d.row.validUntil} tarihinde bitti; tablo statik olduğu için hâlâ bu fiyat kullanılıyor.
            {:else if d.row?.validUntil}
              Bu fiyat {d.row.validUntil} tarihine kadar geçerli (kampanya ya da yönlendirme).
            {/if}
          </p>
          {#if d.row?.source}<p class="muted small source">Kaynak: {d.row.source}</p>{/if}

          <div aria-live="polite">
            {#if error}
              <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{error}</span></p>
            {/if}
          </div>

          <div class="actions">
            <button type="submit" class="btn btn-primary" disabled={busy}>Kaydet</button>
            <button type="button" class="btn" onclick={close}>Vazgeç</button>
            {#if d.row?.override}
              <button type="button" class="btn btn-danger" disabled={busy} onclick={() => reset(d.row!.model)}>
                {d.row.custom ? 'Modeli kaldır' : 'Varsayılana sıfırla'}
              </button>
            {/if}
          </div>
        </form>
      </section>
    {/if}
  {/if}
</div>

<style>
  .unit {
    font-family: var(--font);
    font-size: 0.82rem;
    font-weight: 400;
  }
  .note {
    margin-bottom: 12px;
    font-size: 0.88rem;
  }
  .search {
    max-width: 360px;
    margin-bottom: 12px;
  }
  .callout {
    margin-bottom: 12px;
  }
  form {
    display: grid;
    gap: 12px;
  }
  .small {
    font-size: 0.82rem;
  }
  .source {
    overflow-wrap: anywhere;
  }
  .state {
    padding: 16px 0;
  }
</style>
