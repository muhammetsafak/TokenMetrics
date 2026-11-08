<script lang="ts">
  import { api, errorMessage, type ManualRecord } from '../lib/api'
  import { isEmptyNumber, localInputToMs, msToLocalInput, wholeOrZero } from '../lib/input'
  import { meta } from '../lib/stores'

  interface Props {
    /** The record being edited; null adds a new one. */
    editing?: ManualRecord | null
    onsaved: (r: ManualRecord, wasEdit: boolean) => void
    oncancel?: () => void
  }
  let { editing = null, onsaved, oncancel }: Props = $props()

  const TOKEN_FIELDS = [
    { key: 'inputTokens', label: 'Giriş token' },
    { key: 'outputTokens', label: 'Çıktı token' },
    { key: 'cacheReadTokens', label: 'Önbellek okuma' },
    { key: 'cacheWrite5mTokens', label: 'Önbellek yazma (5 dk)' },
    { key: 'cacheWrite1hTokens', label: 'Önbellek yazma (1 sa)' },
    { key: 'thinkingTokens', label: 'Düşünme token' },
    { key: 'requestCount', label: 'İstek sayısı' },
  ] as const
  type TokenKey = (typeof TOKEN_FIELDS)[number]['key']

  interface Form {
    when: string // datetime-local value
    service: string
    model: string
    project: string
    session: string
    counts: Record<TokenKey, number | null>
    cost: number | null
  }

  const nowLocal = () => msToLocalInput(Date.now())

  function blank(): Form {
    return {
      when: nowLocal(),
      service: '',
      model: '',
      project: '',
      session: '',
      counts: { inputTokens: null, outputTokens: null, cacheReadTokens: null, cacheWrite5mTokens: null, cacheWrite1hTokens: null, thinkingTokens: null, requestCount: null },
      cost: null,
    }
  }

  function fromRecord(r: ManualRecord): Form {
    const z = (v: number) => (v === 0 ? null : v)
    return {
      when: msToLocalInput(r.timestampMs),
      service: r.service,
      model: r.model,
      project: r.project,
      session: r.session,
      counts: {
        inputTokens: z(r.inputTokens),
        outputTokens: z(r.outputTokens),
        cacheReadTokens: z(r.cacheReadTokens),
        cacheWrite5mTokens: z(r.cacheWrite5mTokens),
        cacheWrite1hTokens: z(r.cacheWrite1hTokens),
        thinkingTokens: z(r.thinkingTokens),
        requestCount: z(r.requestCount),
      },
      cost: r.costUsd ?? null,
    }
  }

  // The form is re-seeded whenever a different record is chosen for editing.
  // svelte-ignore state_referenced_locally
  let form = $state<Form>(editing ? fromRecord(editing) : blank())
  // svelte-ignore state_referenced_locally
  let seededFor = $state<string | null>(editing?.externalId ?? null)
  $effect(() => {
    const key = editing?.externalId ?? null
    if (key !== seededFor) {
      seededFor = key
      form = editing ? fromRecord(editing) : blank()
      error = ''
      invalid = ''
    }
  })

  let busy = $state(false)
  let error = $state('')
  let invalid = $state('') // id of the field the error is about

  const serviceOptions = $derived($meta?.services.filter((s) => s !== '') ?? [])
  const modelOptions = $derived($meta?.models.filter((m) => m !== '') ?? [])

  function fail(field: string, message: string): false {
    invalid = field
    error = message
    document.getElementById(field)?.focus()
    return false
  }

  function validate(): ManualRecord | false {
    const f = form
    // An edited time that the user left alone keeps its seconds.
    const keepOriginal = editing && f.when === msToLocalInput(editing.timestampMs)
    const ts = keepOriginal ? editing!.timestampMs : localInputToMs(f.when)
    if (Number.isNaN(ts)) return fail('me-when', 'Geçerli bir tarih ve saat girilmeli.')
    if (ts > Date.now() + 24 * 3600_000) return fail('me-when', 'Tarih ve saat gelecekte olamaz (en fazla 24 saat ilerisi kabul edilir).')
    if (f.service.trim() === '') return fail('me-service', 'Servis girilmeli.')
    if (f.model.trim() === '') return fail('me-model', 'Model girilmeli.')

    // type=number reports unparsable input ("12e") as an empty value; do not read that as 0.
    const bad = (id: string) => (document.getElementById(id) as HTMLInputElement | null)?.validity.badInput === true
    const counts = {} as Record<TokenKey, number>
    for (const t of TOKEN_FIELDS) {
      if (bad(`me-${t.key}`)) return fail(`me-${t.key}`, `${t.label}: geçerli bir tam sayı girilmeli.`)
      const v = wholeOrZero(f.counts[t.key])
      if (v === null) return fail(`me-${t.key}`, `${t.label}: 0 veya daha büyük bir tam sayı girilmeli.`)
      counts[t.key] = v
    }
    if (TOKEN_FIELDS.every((t) => t.key === 'requestCount' || counts[t.key] === 0)) {
      return fail('me-inputTokens', 'En az bir token alanı 0\'dan büyük olmalı.')
    }
    let cost: number | undefined
    if (bad('me-cost')) return fail('me-cost', 'Maliyet geçerli bir sayı olmalı.')
    if (!isEmptyNumber(f.cost)) {
      if (f.cost < 0) return fail('me-cost', 'Maliyet negatif olamaz.')
      cost = f.cost
    }
    return {
      externalId: editing?.externalId ?? '',
      timestampMs: ts,
      service: f.service.trim(),
      model: f.model.trim(),
      project: f.project.trim(),
      session: f.session.trim(),
      ...counts,
      costUsd: cost,
    }
  }

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    error = ''
    invalid = ''
    const rec = validate()
    if (!rec) return
    busy = true
    try {
      const saved = editing ? await api.updateManualRecord(rec) : await api.addManualRecord(rec)
      onsaved(saved, !!editing)
      if (!editing) {
        // Keep the context of a series of entries (time, service, model), clear the numbers.
        form = { ...form, counts: blank().counts, cost: null, session: '' }
      }
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = false
    }
  }

  function reset() {
    form = editing ? fromRecord(editing) : blank()
    error = ''
    invalid = ''
  }
</script>

<form onsubmit={submit} novalidate aria-label={editing ? 'Elle girilen kaydı düzenle' : 'Elle kayıt ekle'}>
  <div class="fields wide">
    <div class="field">
      <label for="me-when">Tarih ve saat</label>
      <input id="me-when" type="datetime-local" required aria-invalid={invalid === 'me-when' ? 'true' : undefined} bind:value={form.when} />
      <span class="hint">Yerel saatinizle.</span>
    </div>
    <div class="field">
      <label for="me-service">Servis</label>
      <input
        id="me-service"
        type="text"
        list="me-services"
        autocomplete="off"
        maxlength="256"
        required
        placeholder="anthropic, openai, google…"
        aria-invalid={invalid === 'me-service' ? 'true' : undefined}
        bind:value={form.service}
      />
      <datalist id="me-services">
        {#each serviceOptions as s (s)}<option value={s}></option>{/each}
      </datalist>
    </div>
    <div class="field">
      <label for="me-model">Model</label>
      <input
        id="me-model"
        type="text"
        list="me-models"
        autocomplete="off"
        maxlength="256"
        required
        aria-invalid={invalid === 'me-model' ? 'true' : undefined}
        bind:value={form.model}
      />
      <datalist id="me-models">
        {#each modelOptions as m (m)}<option value={m}></option>{/each}
      </datalist>
    </div>
    <div class="field">
      <label for="me-project">Proje <span class="muted">(isteğe bağlı)</span></label>
      <input id="me-project" type="text" autocomplete="off" maxlength="256" bind:value={form.project} />
    </div>
    <div class="field">
      <label for="me-session">Oturum <span class="muted">(isteğe bağlı)</span></label>
      <input id="me-session" type="text" autocomplete="off" maxlength="256" bind:value={form.session} />
    </div>
  </div>

  <fieldset>
    <legend>Kullanım</legend>
    <div class="fields">
      {#each TOKEN_FIELDS as t (t.key)}
        <div class="field">
          <label for="me-{t.key}">{t.label}</label>
          <input
            id="me-{t.key}"
            type="number"
            min="0"
            step="1"
            inputmode="numeric"
            placeholder="0"
            aria-invalid={invalid === `me-${t.key}` ? 'true' : undefined}
            bind:value={form.counts[t.key]}
          />
        </div>
      {/each}
      <div class="field">
        <label for="me-cost">Maliyet (USD)</label>
        <input
          id="me-cost"
          type="number"
          min="0"
          step="any"
          inputmode="decimal"
          placeholder="İsteğe bağlı"
          aria-invalid={invalid === 'me-cost' ? 'true' : undefined}
          bind:value={form.cost}
        />
        <span class="hint">Boş bırakılırsa fiyat tablosundan tahmin edilir.</span>
      </div>
    </div>
  </fieldset>

  <div aria-live="polite">
    {#if error}
      <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{error}</span></p>
    {/if}
  </div>

  <div class="actions">
    <button type="submit" class="btn btn-primary" disabled={busy}>{editing ? 'Kaydı güncelle' : 'Kaydı ekle'}</button>
    <button type="button" class="btn" onclick={reset}>Formu temizle</button>
    {#if editing && oncancel}
      <button type="button" class="btn" onclick={oncancel}>Vazgeç</button>
    {/if}
  </div>
</form>

<style>
  form {
    display: grid;
    gap: 14px;
  }
  fieldset {
    display: grid;
    gap: 8px;
    margin: 0;
    padding: 0;
    border: 0;
    min-width: 0;
  }
  legend {
    padding: 0;
    margin-bottom: 8px;
    font-size: 0.85rem;
    font-weight: 600;
    color: var(--text-2);
  }
</style>
