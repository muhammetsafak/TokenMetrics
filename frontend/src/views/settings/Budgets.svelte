<script lang="ts">
  import { api, errorMessage, type Budget } from '../../lib/api'
  import { formatInt, formatUSD, serviceLabel } from '../../lib/format'
  import { isEmptyNumber } from '../../lib/input'
  import { createLoader, dataVersion, meta } from '../../lib/stores'
  import ConfirmButton from '../../components/ConfirmButton.svelte'
  import EstimatedBadge from '../../components/EstimatedBadge.svelte'

  const budgets = createLoader<Budget[]>()
  const reload = () => budgets.load(() => api.budgets())
  $effect(() => {
    reload()
  })

  const SCOPES = { total: 'Toplam', service: 'Servis', model: 'Model' } as const
  const PERIODS = { daily: 'Günlük', monthly: 'Aylık' } as const

  const scopeText = (b: Budget) =>
    b.scope === 'total' ? 'Toplam' : `${SCOPES[b.scope as keyof typeof SCOPES] ?? b.scope}: ${b.scope === 'service' ? serviceLabel(b.scopeValue) : b.scopeValue}`

  // ---- form ----

  let id = $state(0)
  let scope = $state<'total' | 'service' | 'model'>('total')
  let scopeValue = $state('')
  let period = $state<'daily' | 'monthly'>('monthly')
  let tokens = $state<number | null>(null)
  let usd = $state<number | null>(null)
  let enabled = $state(true)
  let busy = $state('')
  let error = $state('')
  let notice = $state('')

  const valueOptions = $derived(scope === 'service' ? ($meta?.services ?? []) : scope === 'model' ? ($meta?.models ?? []) : [])

  function clearForm() {
    id = 0
    scope = 'total'
    scopeValue = ''
    period = 'monthly'
    tokens = null
    usd = null
    enabled = true
    error = ''
  }

  function edit(b: Budget) {
    id = b.id
    scope = b.scope as typeof scope
    scopeValue = b.scopeValue
    period = b.period as typeof period
    tokens = b.thresholdTokens ?? null
    usd = b.thresholdUsd ?? null
    enabled = b.enabled
    error = ''
    notice = ''
    document.getElementById('budget-scope')?.focus()
  }

  const undef = (v: number | null) => (isEmptyNumber(v) ? undefined : v)

  async function persist(b: Budget, done: string): Promise<boolean> {
    try {
      await api.saveBudget(b)
      notice = done
      await reload()
      return true
    } catch (err) {
      error = errorMessage(err)
      return false
    }
  }

  async function save(e: SubmitEvent) {
    e.preventDefault()
    const t = undef(tokens)
    const u = undef(usd)
    if (t === undefined && u === undefined) {
      error = 'Token veya USD eşiklerinden en az biri girilmeli.'
      return
    }
    if (scope !== 'total' && scopeValue.trim() === '') {
      error = scope === 'service' ? 'Servis adı girilmeli.' : 'Model adı girilmeli.'
      return
    }
    busy = 'form'
    error = ''
    notice = ''
    const ok = await persist(
      { id, scope, scopeValue: scope === 'total' ? '' : scopeValue.trim(), period, thresholdTokens: t, thresholdUsd: u, enabled },
      id === 0 ? 'Bütçe eklendi.' : 'Bütçe güncellendi.',
    )
    busy = ''
    if (ok) clearForm()
  }

  async function setEnabled(b: Budget, box: HTMLInputElement) {
    busy = `en-${b.id}`
    error = ''
    notice = ''
    const ok = await persist({ ...b, enabled: box.checked }, `Bütçe ${box.checked ? 'açıldı' : 'kapatıldı'}.`)
    // The checkbox is one-way bound: put it back when the change was not saved.
    if (!ok) box.checked = b.enabled
    busy = ''
  }

  async function remove(b: Budget) {
    busy = `del-${b.id}`
    error = ''
    notice = ''
    try {
      await api.deleteBudget(b.id)
      notice = 'Bütçe silindi.'
      if (id === b.id) clearForm()
      await reload()
      dataVersion.update((n) => n + 1)
    } catch (err) {
      error = errorMessage(err)
    } finally {
      busy = ''
    }
  }
</script>

<div class="view">
  {#if $budgets.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Bütçeler yüklenemedi</h2>
      <p>{$budgets.error}</p>
      <button type="button" class="btn" onclick={reload}>Yeniden dene</button>
    </section>
  {:else if !$budgets.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <section class="card" aria-label="Bütçeler">
      <div class="section-head">
        <h2>Bütçe kuralları <EstimatedBadge title="USD eşikleri tahmini maliyet üzerinden değerlendirilir" /></h2>
      </div>
      <p class="muted note">
        Bir dönemde (gün ya da ay, yerel takvime göre) harcama ya da token eşiğe ulaştığında uyarılırsınız. Her iki eşik de
        girilirse hangisine önce ulaşılırsa o geçerli olur. USD eşiği tahmini maliyete göre hesaplanır; gerçek fatura
        farklı olabilir. Bir kuralın eşiğini ya da kapsamını değiştirirseniz, o dönem için daha önce gönderilmiş uyarı
        sıfırlanır ve kural yeniden uyarabilir.
      </p>

      <div aria-live="polite">
        {#if notice}
          <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{notice}</span></p>
        {/if}
      </div>

      {#if $budgets.data.length === 0}
        <div class="state" aria-live="polite">
          <h2>Henüz bütçe tanımlı değil</h2>
          <p>Aşağıdaki formdan günlük ya da aylık bir kural ekleyin.</p>
        </div>
      {:else}
        <!-- svelte-ignore a11y_no_noninteractive_tabindex -->
        <div class="table-scroll" role="region" aria-label="Tanımlı bütçeler" tabindex="0">
          <table>
            <caption class="sr-only">Tanımlı bütçeler</caption>
            <thead>
              <tr>
                <th scope="col">Kapsam</th>
                <th scope="col">Dönem</th>
                <th scope="col" class="num">Token eşiği</th>
                <th scope="col" class="num">USD eşiği</th>
                <th scope="col">Durum</th>
                <th scope="col"><span class="sr-only">İşlemler</span></th>
              </tr>
            </thead>
            <tbody>
              {#each $budgets.data as b (b.id)}
                <tr>
                  <td class="wrap">{scopeText(b)}</td>
                  <td>{PERIODS[b.period as keyof typeof PERIODS] ?? b.period}</td>
                  <td class="num">{b.thresholdTokens == null ? '—' : formatInt(b.thresholdTokens)}</td>
                  <td class="num">{b.thresholdUsd == null ? '—' : formatUSD(b.thresholdUsd)}</td>
                  <td>
                    <label class="check">
                      <input
                        type="checkbox"
                        checked={b.enabled}
                        disabled={busy === `en-${b.id}`}
                        onchange={(e) => setEnabled(b, e.currentTarget)}
                      />
                      {b.enabled ? 'Açık' : 'Kapalı'}<span class="sr-only"> — {scopeText(b)}, {PERIODS[b.period as keyof typeof PERIODS]}</span>
                    </label>
                  </td>
                  <td class="row-actions">
                    <div class="actions">
                      <button type="button" class="btn btn-small" onclick={() => edit(b)}>
                        Düzenle<span class="sr-only"> — {scopeText(b)}, {PERIODS[b.period as keyof typeof PERIODS]}</span>
                      </button>
                      <ConfirmButton
                        label="Sil"
                        target="{scopeText(b)}, {PERIODS[b.period as keyof typeof PERIODS]} bütçesi"
                        disabled={busy === `del-${b.id}`}
                        onconfirm={() => remove(b)}
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
        <h3>{id === 0 ? 'Bütçe ekle' : 'Bütçeyi düzenle'}</h3>
        <div class="fields">
          <div class="field">
            <label for="budget-scope">Kapsam</label>
            <select id="budget-scope" bind:value={scope}>
              <option value="total">Toplam (tüm kullanım)</option>
              <option value="service">Tek servis</option>
              <option value="model">Tek model</option>
            </select>
          </div>
          {#if scope !== 'total'}
            <div class="field">
              <label for="budget-value">{scope === 'service' ? 'Servis' : 'Model'}</label>
              <input id="budget-value" type="text" list="budget-values" autocomplete="off" maxlength="256" required bind:value={scopeValue} />
              <datalist id="budget-values">
                {#each valueOptions as v (v)}<option value={v}></option>{/each}
              </datalist>
            </div>
          {/if}
          <div class="field">
            <label for="budget-period">Dönem</label>
            <select id="budget-period" bind:value={period}>
              <option value="daily">Günlük</option>
              <option value="monthly">Aylık</option>
            </select>
          </div>
          <div class="field">
            <label for="budget-tokens">Token eşiği</label>
            <input id="budget-tokens" type="number" min="1" step="1" inputmode="numeric" placeholder="İsteğe bağlı" bind:value={tokens} />
          </div>
          <div class="field">
            <label for="budget-usd">USD eşiği</label>
            <input id="budget-usd" type="number" min="0.01" step="0.01" inputmode="decimal" placeholder="İsteğe bağlı" bind:value={usd} />
          </div>
        </div>
        <label class="check"><input type="checkbox" bind:checked={enabled} /> Kural açık</label>
        <div aria-live="polite">
          {#if error}
            <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{error}</span></p>
          {/if}
        </div>
        <div class="actions">
          <button type="submit" class="btn btn-primary" disabled={busy === 'form'}>{id === 0 ? 'Ekle' : 'Güncelle'}</button>
          {#if id !== 0}
            <button type="button" class="btn" onclick={clearForm}>Vazgeç</button>
          {/if}
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
</style>
