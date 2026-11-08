<script lang="ts">
  import '../lib/forms.css'
  import { tick } from 'svelte'
  import { api, errorMessage, type ExportResult, type ImportResult, type ManualList, type ManualRecord } from '../lib/api'
  import { formatDateTime, formatInt, formatUSD, serviceLabel } from '../lib/format'
  import { dayInputToMs } from '../lib/input'
  import { createLoader, dataVersion } from '../lib/stores'
  import ConfirmButton from '../components/ConfirmButton.svelte'
  import ManualEntryForm from '../components/ManualEntryForm.svelte'

  // ---- manual records ----

  const manual = createLoader<ManualList>()
  const reloadManual = () => manual.load(() => api.manualRecords())
  $effect(() => {
    void $dataVersion
    reloadManual()
  })

  let editing = $state<ManualRecord | null>(null)
  let manualNotice = $state('')
  let manualError = $state('')
  let formCard: HTMLElement | undefined = $state()

  const bump = () => dataVersion.update((n) => n + 1)

  function onSaved(r: ManualRecord, wasEdit: boolean) {
    manualNotice = wasEdit ? 'Kayıt güncellendi.' : `Kayıt eklendi: ${serviceLabel(r.service)} / ${r.model}. Özet ekranına yansıdı.`
    manualError = ''
    if (wasEdit) editing = null
    bump()
  }

  async function startEdit(r: ManualRecord) {
    editing = r
    manualNotice = ''
    await tick()
    formCard?.scrollIntoView({ block: 'nearest' })
    document.getElementById('me-when')?.focus()
  }

  async function remove(r: ManualRecord) {
    manualError = ''
    manualNotice = ''
    try {
      await api.deleteManualRecord(r.externalId)
      if (editing?.externalId === r.externalId) editing = null
      manualNotice = 'Kayıt silindi.'
      bump()
    } catch (e) {
      manualError = errorMessage(e)
    }
  }

  const cacheTotal = (r: ManualRecord) => r.cacheReadTokens + r.cacheWrite5mTokens + r.cacheWrite1hTokens

  // ---- CSV import ----

  const CSV_COLUMNS = [
    { name: 'timestamp', required: true, text: 'Kullanımın zamanı. RFC 3339 önerilir: 2026-03-01T10:00:00Z. Saat dilimi yoksa UTC kabul edilir.' },
    { name: 'service', required: true, text: 'Serbest metin: anthropic, openai, my-internal-api…' },
    { name: 'model', required: true, text: 'Model adı.' },
    { name: 'input_tokens', required: false, text: '0 veya daha büyük tam sayı; boş = 0.' },
    { name: 'output_tokens', required: false, text: 'Aynı.' },
    { name: 'cache_read_tokens', required: false, text: 'Aynı.' },
    { name: 'cache_write_tokens', required: false, text: 'Aynı; 5 dakikalık önbellek yazması olarak saklanır.' },
    { name: 'project', required: false, text: 'Serbest metin.' },
    { name: 'session', required: false, text: 'Serbest metin.' },
    { name: 'cost_usd', required: false, text: 'Kaynağın bildirdiği maliyet (USD, ondalık ayracı nokta); boş = bildirilmedi.' },
  ]
  const CSV_EXAMPLE = `timestamp,service,model,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,project,session,cost_usd
2026-03-01T10:00:00Z,anthropic,claude-sonnet,1000,500,200,100,app,s1,0.0123
2026-03-02,google,gemini-pro,300,,,,,,`

  let csvBusy = $state(false)
  let csvResult = $state<ImportResult | null>(null)
  let csvError = $state('')

  async function importCsv() {
    csvBusy = true
    csvError = ''
    csvResult = null
    try {
      const r = await api.importCsv()
      if (!r.canceled) {
        csvResult = r
        if (r.added > 0 || r.updated > 0) bump()
      }
    } catch (e) {
      csvError = errorMessage(e)
    } finally {
      csvBusy = false
    }
  }

  // ---- export / JSON import ----

  let format = $state<'json' | 'csv'>('json')
  let fromDay = $state('')
  let toDay = $state('')
  let exportBusy = $state(false)
  let exportResult = $state<ExportResult | null>(null)
  let exportError = $state('')

  const rangeInvalid = $derived(fromDay !== '' && toDay !== '' && dayInputToMs(fromDay) > dayInputToMs(toDay))

  async function doExport() {
    if (rangeInvalid) {
      exportError = 'Başlangıç tarihi bitiş tarihinden sonra olamaz.'
      return
    }
    exportBusy = true
    exportError = ''
    exportResult = null
    try {
      const from = dayInputToMs(fromDay)
      const to = dayInputToMs(toDay, 1) // the end day is inclusive
      const r = await api.exportData(format, {
        fromMs: Number.isNaN(from) ? 0 : from,
        toMs: Number.isNaN(to) ? 0 : to,
        sources: [],
        services: [],
        models: [],
        projects: [],
        sessions: [],
      })
      if (!r.canceled) exportResult = r
    } catch (e) {
      exportError = errorMessage(e)
    } finally {
      exportBusy = false
    }
  }

  let jsonBusy = $state(false)
  let jsonResult = $state<ImportResult | null>(null)
  let jsonError = $state('')

  async function importJson() {
    jsonBusy = true
    jsonError = ''
    jsonResult = null
    try {
      const r = await api.importJson()
      if (!r.canceled) {
        jsonResult = r
        if (r.added > 0 || r.updated > 0) bump()
      }
    } catch (e) {
      jsonError = errorMessage(e)
    } finally {
      jsonBusy = false
    }
  }
</script>

{#snippet importSummary(r: ImportResult, what: string)}
  <div class="result" aria-live="polite">
    <p class="callout {r.errorCount > 0 ? 'warn' : 'ok'}">
      <span aria-hidden="true">{r.errorCount > 0 ? '▲' : '●'}</span>
      <span class="body">
        <strong>{r.file}</strong>: {formatInt(r.rows)} {what} okundu — {formatInt(r.added)} eklendi{#if r.updated > 0}, {formatInt(r.updated)} güncellendi{/if}{#if r.unchanged > 0}, {formatInt(r.unchanged)} zaten kayıtlıydı (değişmedi){/if}{#if r.errorCount > 0}, {formatInt(r.errorCount)} reddedildi{/if}.
      </span>
    </p>
    {#if r.errorCount > 0}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-scroll errs" role="region" aria-label="Reddedilen satırlar" tabindex="0">
        <table>
          <caption class="sr-only">Reddedilen satırlar ve nedenleri</caption>
          <thead>
            <tr><th scope="col" class="num">Satır</th><th scope="col">Neden</th></tr>
          </thead>
          <tbody>
            {#each r.errors as e, i (`${e.line}-${i}`)}
              <tr><td class="num">{e.line}</td><td class="wrap">{e.reason}</td></tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if r.errorCount > r.errors.length}
        <p class="muted small">İlk {formatInt(r.errors.length)} hata gösteriliyor; toplam {formatInt(r.errorCount)}.</p>
      {/if}
      <p class="muted small">Reddedilen satırlar hiç kaydedilmedi; dosyayı düzeltip yeniden içe aktarabilirsiniz (kayıtlı satırlar tekrar eklenmez).</p>
    {/if}
  </div>
{/snippet}

<div class="view">
  <section class="card" aria-label={editing ? 'Kaydı düzenle' : 'Elle kayıt ekle'} bind:this={formCard}>
    <div class="section-head">
      <h2>{editing ? 'Elle girilen kaydı düzenle' : 'Elle kayıt ekle'}</h2>
    </div>
    <p class="muted note">
      Günlük bırakmayan bir servisin (web arayüzü, başka bir araç) kullanımını buradan ekleyebilirsiniz. Kayıt "Elle giriş"
      kaynağıyla saklanır ve Özet ekranındaki toplamlara dahil olur.
    </p>
    <ManualEntryForm {editing} onsaved={onSaved} oncancel={() => (editing = null)} />
    <div aria-live="polite" class="msgs">
      {#if manualNotice}
        <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{manualNotice}</span></p>
      {/if}
    </div>
  </section>

  <section class="card" aria-label="Elle girilen kayıtlar">
    <div class="section-head">
      <h2>Elle girilen kayıtlar</h2>
      {#if $manual.data}<span class="muted small">{formatInt($manual.data.total)} kayıt</span>{/if}
    </div>
    {#if manualError}
      <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{manualError}</span></p>
    {/if}
    {#if $manual.status === 'error'}
      <div class="state error" role="alert">
        <h2>Kayıtlar yüklenemedi</h2>
        <p>{$manual.error}</p>
        <button type="button" class="btn" onclick={reloadManual}>Yeniden dene</button>
      </div>
    {:else if !$manual.data}
      <p class="muted" aria-busy="true">Yükleniyor…</p>
    {:else if $manual.data.rows.length === 0}
      <div class="state" aria-live="polite">
        <h2>Henüz elle kayıt yok</h2>
        <p>Yukarıdaki formla ilk kaydı ekleyin.</p>
      </div>
    {:else}
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-scroll" role="region" aria-label="Elle girilen kayıtlar tablosu" tabindex="0">
        <table>
          <caption class="sr-only">Elle girilen kayıtlar, en yeniden eskiye</caption>
          <thead>
            <tr>
              <th scope="col">Zaman</th>
              <th scope="col">Servis</th>
              <th scope="col">Model</th>
              <th scope="col" class="num">Giriş</th>
              <th scope="col" class="num">Çıktı</th>
              <th scope="col" class="num">Önbellek</th>
              <th scope="col" class="num">Maliyet</th>
              <th scope="col"><span class="sr-only">İşlemler</span></th>
            </tr>
          </thead>
          <tbody>
            {#each $manual.data.rows as r (r.externalId)}
              <tr>
                <td>{formatDateTime(r.timestampMs)}</td>
                <td>{serviceLabel(r.service)}</td>
                <td class="wrap">{r.model}</td>
                <td class="num">{formatInt(r.inputTokens)}</td>
                <td class="num">{formatInt(r.outputTokens)}</td>
                <td class="num">{formatInt(cacheTotal(r))}</td>
                <td class="num">{r.costUsd == null ? '—' : formatUSD(r.costUsd)}</td>
                <td class="row-actions">
                  <div class="actions">
                    <button type="button" class="btn btn-small" onclick={() => startEdit(r)}>
                      Düzenle<span class="sr-only"> — {r.model}, {formatDateTime(r.timestampMs)}</span>
                    </button>
                    <ConfirmButton label="Sil" target="{r.model}, {formatDateTime(r.timestampMs)} kaydı" onconfirm={() => remove(r)} />
                  </div>
                </td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      {#if $manual.data.truncated}
        <p class="muted small">En yeni {formatInt($manual.data.rows.length)} kayıt gösteriliyor; toplam {formatInt($manual.data.total)}.</p>
      {/if}
    {/if}
  </section>

  <section class="card" aria-label="CSV içe aktar">
    <div class="section-head"><h2>CSV içe aktar</h2></div>

    <div class="callout warn">
      <span aria-hidden="true">▲</span>
      <div class="body">
        <p>
          <strong>Bu uygulamadan aldığınız bir CSV dışa aktarmasını aynı veritabanına geri içe aktarmayın.</strong> CSV
          dosyası kaydın kimliğini taşımaz; içe aktarma kayıtları tanıyamaz ve her satırı yeni kayıt sayar, toplamlarınız
          ikiye katlanır.
        </p>
        <p>
          Verinizi bilgisayarlar arasında taşımak ya da birleştirmek için aşağıdaki <strong>JSON dışa/içe aktarmayı</strong> kullanın;
          kayıt kimliğini korur ve aynı kaydı iki kez saymaz. CSV, başka bir araçtan gelen veriyi içe aktarmak içindir.
        </p>
      </div>
    </div>

    <details class="format">
      <summary>CSV biçimi</summary>
      <p class="muted small">
        İlk satır başlık olmalı. Kolon sırası serbest, adlar büyük/küçük harfe duyarsız, ayraç <code>,</code> ya da <code>;</code>
        (başlıktan anlaşılır). Bilinmeyen ya da tekrar eden kolon adı dosyanın tamamını reddettirir. Satır başına en az bir
        token kolonu 0'dan büyük olmalı. En fazla 50 MiB.
      </p>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <div class="table-scroll" role="region" aria-label="CSV kolonları" tabindex="0">
        <table>
          <caption class="sr-only">CSV kolonları</caption>
          <thead>
            <tr><th scope="col">Kolon</th><th scope="col">Zorunlu</th><th scope="col">Anlamı</th></tr>
          </thead>
          <tbody>
            {#each CSV_COLUMNS as c (c.name)}
              <tr>
                <td class="mono">{c.name}</td>
                <td>{c.required ? 'evet' : 'hayır'}</td>
                <td class="wrap">{c.text}</td>
              </tr>
            {/each}
          </tbody>
        </table>
      </div>
      <p class="muted small">Örnek:</p>
      <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
      <pre class="example" tabindex="0" aria-label="Örnek CSV">{CSV_EXAMPLE}</pre>
      <ul class="muted small notes">
        <li>Aynı dosyayı iki kez içe aktarmak hiçbir şeyi değiştirmez; her satır içeriğinden türeyen bir kimlikle tanınır.</li>
        <li>Birebir aynı iki satır tek kayıt sayılır; gerçekten iki çağrı yaptıysanız satırları farklılaştırın (örneğin farklı <code>session</code>).</li>
        <li>Bir satırın değerini düzeltip yeniden içe aktarırsanız eski kayıt silinmez, yanına yenisi eklenir.</li>
      </ul>
    </details>

    <div class="actions pad">
      <button type="button" class="btn btn-primary" disabled={csvBusy} onclick={importCsv}>
        {csvBusy ? 'İçe aktarılıyor…' : 'CSV dosyası seç ve içe aktar…'}
      </button>
    </div>
    {#if csvError}
      <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{csvError}</span></p>
    {/if}
    {#if csvResult}{@render importSummary(csvResult, 'satır')}{/if}
  </section>

  <section class="card" aria-label="Dışa aktar ve JSON içe aktar">
    <div class="section-head"><h2>Dışa aktar</h2></div>
    <p class="muted note">
      Kayıtlarınızı bir dosyaya yazar. Ayarlar, API anahtarları ve diğer gizli bilgiler hiçbir dışa aktarmaya girmez.
    </p>
    <form
      class="export"
      onsubmit={(e) => {
        e.preventDefault()
        void doExport()
      }}
    >
      <fieldset>
        <legend>Biçim</legend>
        <label class="check"><input type="radio" name="fmt" value="json" bind:group={format} /> JSON <span class="muted">(önerilir: kayıpsız, bilgisayarlar arası taşımaya uygun)</span></label>
        <label class="check"><input type="radio" name="fmt" value="csv" bind:group={format} /> CSV <span class="muted">(tablo programları için; bazı alanlar eksik)</span></label>
      </fieldset>
      {#if format === 'csv'}
        <p class="callout">
          <span aria-hidden="true">●</span>
          <span class="body">
            CSV'de önbellek yazma 5 dk ve 1 sa toplamı tek kolondadır; düşünme token'ı, istek sayısı, kaynak ve cihaz yer almaz.
            Bu dosyayı aynı veritabanına geri içe aktarmayın.
          </span>
        </p>
      {/if}
      <div class="fields">
        <div class="field">
          <label for="ex-from">Başlangıç tarihi <span class="muted">(isteğe bağlı)</span></label>
          <input id="ex-from" type="date" bind:value={fromDay} />
        </div>
        <div class="field">
          <label for="ex-to">Bitiş tarihi <span class="muted">(dahil, isteğe bağlı)</span></label>
          <input id="ex-to" type="date" aria-invalid={rangeInvalid ? 'true' : undefined} bind:value={toDay} />
        </div>
      </div>
      <p class="muted small">Tarih seçmezseniz tüm kayıtlar dışa aktarılır.</p>
      <div class="actions">
        <button type="submit" class="btn btn-primary" disabled={exportBusy || rangeInvalid}>
          {exportBusy ? 'Yazılıyor…' : 'Dışa aktar…'}
        </button>
      </div>
      <div aria-live="polite">
        {#if rangeInvalid}
          <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">Başlangıç tarihi bitiş tarihinden sonra olamaz.</span></p>
        {:else if exportError}
          <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{exportError}</span></p>
        {:else if exportResult}
          <p class="callout ok">
            <span aria-hidden="true">●</span>
            <span class="body">{formatInt(exportResult.records)} kayıt {exportResult.format.toUpperCase()} olarak kaydedildi: <span class="mono">{exportResult.path}</span></span>
          </p>
        {/if}
      </div>
    </form>
  </section>

  <section class="card" aria-label="JSON içe aktar">
    <div class="section-head"><h2>JSON içe aktar</h2></div>
    <p class="muted note">
      Başka bir bilgisayardan (ya da yedekten) aldığınız JSON dışa aktarmasını mevcut verinizle birleştirir. Kayıt
      kimlikleri korunduğu için aynı dosyayı tekrar içe aktarmak hiçbir şeyi iki kez saymaz.
    </p>
    <p class="callout warn">
      <span aria-hidden="true">▲</span>
      <span class="body">
        Dosyadaki bir kayıt zaten varsa dosyadaki değerler onun üzerine yazılır (son yazan kazanır). Eski bir dışa
        aktarmayı içe aktarmak, bir taramanın sonradan güncellediği bir kaydı eski değerine döndürebilir.
      </span>
    </p>
    <div class="actions pad">
      <button type="button" class="btn btn-primary" disabled={jsonBusy} onclick={importJson}>
        {jsonBusy ? 'İçe aktarılıyor…' : 'JSON dosyası seç ve içe aktar…'}
      </button>
    </div>
    {#if jsonError}
      <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{jsonError}</span></p>
    {/if}
    {#if jsonResult}{@render importSummary(jsonResult, 'kayıt')}{/if}
  </section>
</div>

<style>
  .note {
    margin-bottom: 12px;
    font-size: 0.88rem;
  }
  .small {
    font-size: 0.82rem;
  }
  .msgs {
    margin-top: 12px;
  }
  .msgs:empty {
    display: none;
  }
  .state {
    padding: 8px 0 0;
  }
  .callout {
    margin-bottom: 12px;
  }
  .actions.pad {
    margin: 4px 0 12px;
  }
  .format {
    margin-bottom: 12px;
  }
  .format summary {
    cursor: pointer;
    font-weight: 500;
    padding: 4px 0;
  }
  .format > * + * {
    margin-top: 10px;
  }
  .example {
    margin: 0;
    padding: 10px 12px;
    overflow-x: auto;
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: var(--radius-sm);
    font-family: var(--font-mono);
    font-size: 0.8rem;
    white-space: pre;
  }
  .notes {
    margin: 0;
    padding-left: 18px;
  }
  .result {
    display: grid;
    gap: 8px;
  }
  .result .callout {
    margin-bottom: 0;
  }
  .errs {
    max-height: 320px;
    overflow-y: auto;
  }
  .export {
    display: grid;
    gap: 12px;
  }
  .export .callout {
    margin-bottom: 0;
  }
  fieldset {
    display: grid;
    gap: 4px;
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  legend {
    padding: 0;
    margin-bottom: 4px;
    font-size: 0.85rem;
    font-weight: 500;
    color: var(--text-2);
  }
  code {
    font-family: var(--font-mono);
    font-size: 0.9em;
  }
</style>
