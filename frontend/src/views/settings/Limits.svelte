<script lang="ts">
  import { api, errorMessage, type LimitSetting, type LimitSettingsData } from '../../lib/api'
  import { formatInt, LIMIT_KIND_LABELS, localTimeZone, METRIC_LABELS, serviceLabel } from '../../lib/format'
  import { isEmptyNumber } from '../../lib/input'
  import { createLoader, dataVersion, meta } from '../../lib/stores'
  import ConfirmButton from '../../components/ConfirmButton.svelte'
  import EstimatedBadge from '../../components/EstimatedBadge.svelte'

  const data = createLoader<LimitSettingsData>()
  const reload = () => data.load(() => api.limitSettings())
  $effect(() => {
    reload()
  })

  const KNOWN_SERVICES = ['anthropic', 'openai', 'google']
  const serviceOptions = $derived([...new Set([...KNOWN_SERVICES, ...($meta?.services ?? []).filter((s) => s !== '')])])

  const WEEKDAYS = [
    { v: 1, label: 'Pazartesi' },
    { v: 2, label: 'Salı' },
    { v: 3, label: 'Çarşamba' },
    { v: 4, label: 'Perşembe' },
    { v: 5, label: 'Cuma' },
    { v: 6, label: 'Cumartesi' },
    { v: 0, label: 'Pazar' },
  ]
  const weekdayLabel = (v: number) => WEEKDAYS.find((w) => w.v === v)?.label ?? String(v)
  const hours = Array.from({ length: 24 }, (_, h) => h)
  const hourLabel = (h: number) => `${String(h).padStart(2, '0')}:00`

  const valueText = (l: LimitSetting) =>
    l.metric === 'cost_usd' ? `$${l.limit.toLocaleString('en-US', { minimumFractionDigits: 2, maximumFractionDigits: 2 })}` : `${formatInt(l.limit)} token`

  // ---- limit form ----

  let service = $state('')
  let kind = $state<'5h' | 'weekly'>('5h')
  let metric = $state<'total_tokens' | 'output_tokens' | 'cost_usd'>('total_tokens')
  let value = $state<number | null>(null)
  /** ID of the limit being edited (0 = adding). Its service and window are locked. */
  let editingId = $state(0)
  const editing = $derived(editingId !== 0)
  let busy = $state('')
  let error = $state('')
  let notice = $state('')

  // A limit exists once per (service, kind): the form shows whether saving adds or replaces.
  const existing = $derived(
    $data.data?.limits.find((l) => l.service === service.trim() && l.kind === kind) ?? null,
  )

  function edit(l: LimitSetting) {
    service = l.service
    kind = l.kind as '5h' | 'weekly'
    metric = l.metric as typeof metric
    value = l.limit
    editingId = l.id
    error = ''
    notice = ''
    document.getElementById('limit-service')?.focus()
  }

  function clearForm() {
    service = ''
    kind = '5h'
    metric = 'total_tokens'
    value = null
    editingId = 0
    error = ''
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    if (isEmptyNumber(value) || value <= 0) {
      error = 'Limit değeri 0\'dan büyük olmalı.'
      return
    }
    busy = 'limit'
    error = ''
    notice = ''
    try {
      const saved = await api.saveLimit({ id: editingId, service: service.trim(), kind, metric, limit: value })
      notice = `${saved.service ? serviceLabel(saved.service) : 'Tüm servisler'} — ${LIMIT_KIND_LABELS[saved.kind]} limiti kaydedildi.`
      clearForm()
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  async function remove(l: LimitSetting) {
    busy = `del-${l.id}`
    error = ''
    notice = ''
    try {
      await api.deleteLimit(l.id)
      notice = 'Limit silindi.'
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }

  // ---- weekly start ----

  let weekday = $state(1)
  let hour = $state(0)
  let tzMode = $state<'local' | 'custom'>('local')
  let tzName = $state('')
  let wsError = $state('')
  let wsNotice = $state('')
  let wsSeeded = $state(false)

  // Seed the form from the stored value once; later reloads must not overwrite edits.
  $effect(() => {
    const ws = $data.data?.weeklyStart
    if (ws && !wsSeeded) {
      wsSeeded = true
      weekday = ws.weekday
      hour = ws.hour
      tzMode = ws.timezone ? 'custom' : 'local'
      tzName = ws.timezone
    }
  })

  async function saveWeekly(e: SubmitEvent) {
    e.preventDefault()
    busy = 'weekly'
    wsError = ''
    wsNotice = ''
    try {
      await api.setWeeklyStart({ weekday, hour, timezone: tzMode === 'custom' ? tzName.trim() : '' })
      wsNotice = 'Haftalık pencere başlangıcı kaydedildi.'
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      wsError = errorMessage(err)
    } finally {
      busy = ''
    }
  }
</script>

<div class="view">
  {#if $data.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Limit ayarları yüklenemedi</h2>
      <p>{$data.error}</p>
      <button type="button" class="btn" onclick={reload}>Yeniden dene</button>
    </section>
  {:else if !$data.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <section class="card" aria-label="Limitler">
      <div class="section-head">
        <h2>Abonelik limitleri <EstimatedBadge title="Abonelik limitleri resmî olarak yayınlanmaz; girdiğiniz değerler tahminidir" /></h2>
      </div>
      <p class="muted note">
        Sağlayıcılar abonelik limitlerini kesin olarak yayınlamaz; buraya kendi tahmininizi girersiniz. Limitler Limitler
        ekranında mevcut pencerelerdeki tüketimle karşılaştırılır.
      </p>

      <div aria-live="polite">
        {#if notice}
          <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{notice}</span></p>
        {/if}
      </div>

      {#if $data.data.limits.length === 0}
        <div class="state" aria-live="polite">
          <h2>Henüz limit tanımlı değil</h2>
          <p>Aşağıdaki formdan bir servis ve pencere türü için limit ekleyin.</p>
        </div>
      {:else}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <div class="table-scroll" role="region" aria-label="Tanımlı limitler" tabindex="0">
          <table>
            <caption class="sr-only">Tanımlı limitler</caption>
            <thead>
              <tr>
                <th scope="col">Servis</th>
                <th scope="col">Pencere</th>
                <th scope="col">Ölçüt</th>
                <th scope="col" class="num">Limit</th>
                <th scope="col"><span class="sr-only">İşlemler</span></th>
              </tr>
            </thead>
            <tbody>
              {#each $data.data.limits as l (l.id)}
                <tr>
                  <td>{l.service ? serviceLabel(l.service) : 'Tüm servisler'}</td>
                  <td>{LIMIT_KIND_LABELS[l.kind] ?? l.kind}</td>
                  <td>{METRIC_LABELS[l.metric] ?? l.metric}</td>
                  <td class="num">{valueText(l)}</td>
                  <td class="row-actions">
                    <div class="actions">
                      <button type="button" class="btn btn-small" onclick={() => edit(l)}>
                        Düzenle<span class="sr-only"> — {l.service || 'tüm servisler'}, {LIMIT_KIND_LABELS[l.kind]}</span>
                      </button>
                      <ConfirmButton
                        label="Sil"
                        target="{l.service || 'tüm servisler'}, {LIMIT_KIND_LABELS[l.kind]} limiti"
                        disabled={busy === `del-${l.id}`}
                        onconfirm={() => remove(l)}
                      />
                    </div>
                  </td>
                </tr>
              {/each}
            </tbody>
          </table>
        </div>
      {/if}

      <form onsubmit={save} class="form">
        <h3>{editing ? 'Limiti düzenle' : 'Limit ekle'}</h3>
        <div class="fields">
          <div class="field">
            <label for="limit-service">Servis</label>
            <input id="limit-service" type="text" list="limit-services" autocomplete="off" maxlength="256" placeholder="Boş = tüm servisler" readonly={editing} aria-describedby={editing ? 'limit-lock' : undefined} bind:value={service} />
            <datalist id="limit-services">
              {#each serviceOptions as s (s)}<option value={s}></option>{/each}
            </datalist>
          </div>
          <div class="field">
            <label for="limit-kind">Pencere</label>
            <select id="limit-kind" disabled={editing} aria-describedby={editing ? 'limit-lock' : undefined} bind:value={kind}>
              <option value="5h">{LIMIT_KIND_LABELS['5h']}</option>
              <option value="weekly">{LIMIT_KIND_LABELS.weekly}</option>
            </select>
          </div>
          <div class="field">
            <label for="limit-metric">Ölçüt</label>
            <select id="limit-metric" bind:value={metric}>
              <option value="total_tokens">Toplam token</option>
              <option value="output_tokens">Çıktı token</option>
              <option value="cost_usd">Tahmini maliyet (USD)</option>
            </select>
          </div>
          <div class="field">
            <label for="limit-value">Limit ({metric === 'cost_usd' ? 'USD' : 'token'})</label>
            <input
              id="limit-value"
              type="number"
              min={metric === 'cost_usd' ? '0.01' : '1'}
              step={metric === 'cost_usd' ? '0.01' : '1'}
              inputmode={metric === 'cost_usd' ? 'decimal' : 'numeric'}
              required
              aria-invalid={error ? 'true' : undefined}
              aria-describedby={error ? 'limit-error' : undefined}
              bind:value
            />
          </div>
        </div>
        {#if editing}
          <p id="limit-lock" class="muted small">Düzenlerken servis ve pencere değiştirilemez; bunları değiştirmek için limiti silip yeniden ekleyin. Ölçüt ve değer değiştirilebilir.</p>
        {:else if existing}
          <p class="muted small">Bu servis ve pencere için bir limit zaten var; kaydetmek onu günceller ({valueText(existing)} → yeni değer).</p>
        {/if}
        <div id="limit-error" aria-live="polite">
          {#if error}
            <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{error}</span></p>
          {/if}
        </div>
        <div class="actions">
          <button type="submit" class="btn btn-primary" disabled={busy === 'limit'}>{existing || editing ? 'Güncelle' : 'Ekle'}</button>
          {#if editing || value !== null || service !== ''}
            <button type="button" class="btn" onclick={clearForm}>Formu temizle</button>
          {/if}
        </div>
      </form>
    </section>

    <section class="card" aria-label="Haftalık pencere">
      <div class="section-head"><h2>Haftalık pencere başlangıcı</h2></div>
      <p class="muted note">
        Haftalık limit penceresi burada seçtiğiniz gün ve saatte başlar ve yedi gün sürer. Şu an:
        {weekdayLabel($data.data.weeklyStart.weekday)} {hourLabel($data.data.weeklyStart.hour)}
        ({$data.data.weeklyStart.timezone || `bu bilgisayarın saat dilimi, ${localTimeZone()}`}).
      </p>
      <form onsubmit={saveWeekly} class="form">
        <div class="fields">
          <div class="field">
            <label for="ws-day">Gün</label>
            <select id="ws-day" bind:value={weekday}>
              {#each WEEKDAYS as w (w.v)}<option value={w.v}>{w.label}</option>{/each}
            </select>
          </div>
          <div class="field">
            <label for="ws-hour">Saat</label>
            <select id="ws-hour" bind:value={hour}>
              {#each hours as h (h)}<option value={h}>{hourLabel(h)}</option>{/each}
            </select>
          </div>
        </div>
        <fieldset>
          <legend>Saat dilimi</legend>
          <label class="check"><input type="radio" name="ws-tz" value="local" bind:group={tzMode} /> Bu bilgisayarın saat dilimi ({localTimeZone()})</label>
          <label class="check"><input type="radio" name="ws-tz" value="custom" bind:group={tzMode} /> Başka bir saat dilimi</label>
          {#if tzMode === 'custom'}
            <div class="field tz">
              <label for="ws-tz-name">IANA adı</label>
              <input id="ws-tz-name" type="text" autocomplete="off" placeholder="Europe/Istanbul" bind:value={tzName} />
              <span class="hint">Örnek: Europe/Istanbul, America/New_York, UTC</span>
            </div>
          {/if}
        </fieldset>
        <div aria-live="polite">
          {#if wsError}
            <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{wsError}</span></p>
          {:else if wsNotice}
            <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{wsNotice}</span></p>
          {/if}
        </div>
        <div class="actions">
          <button type="submit" class="btn btn-primary" disabled={busy === 'weekly'}>Kaydet</button>
        </div>
      </form>
    </section>
  {/if}
</div>

<style>
  .note {
    margin-bottom: 12px;
    font-size: 0.88rem;
  }
  .small {
    font-size: 0.82rem;
  }
  .form {
    display: grid;
    gap: 12px;
    margin-top: 16px;
  }
  .form h3 {
    font-family: var(--font-display);
    font-size: 16px;
    font-weight: 600;
  }
  .callout {
    margin-bottom: 12px;
  }
  .form .callout {
    margin-bottom: 0;
  }
  .state {
    padding: 8px 0 0;
  }
  fieldset {
    display: grid;
    gap: 6px;
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
  .tz {
    max-width: 320px;
    margin-left: 24px;
  }
</style>
