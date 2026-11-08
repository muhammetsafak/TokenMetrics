<script lang="ts">
  import { api, errorMessage, type AdminKeysData } from '../../lib/api'
  import { createLoader, refreshMeta } from '../../lib/stores'
  import ConfirmButton from '../../components/ConfirmButton.svelte'

  const keys = createLoader<AdminKeysData>()
  const reload = () => keys.load(() => api.adminKeys())
  $effect(() => {
    reload()
  })

  const HELP: Record<string, { where: string; placeholder: string }> = {
    anthropic: {
      where: 'Anthropic Console → Settings → Admin keys',
      placeholder: 'sk-ant-admin…',
    },
    openai: {
      where: 'OpenAI Platform → Settings → Organization → Admin keys',
      placeholder: 'sk-admin-…',
    },
  }

  // One entry per provider. The typed key lives only here until it is sent.
  let drafts = $state<Record<string, string>>({})
  let busy = $state('')
  let errors = $state<Record<string, string>>({})
  let notices = $state<Record<string, string>>({})

  async function save(provider: string) {
    const key = (drafts[provider] ?? '').trim()
    if (!key) {
      errors[provider] = 'Anahtar boş olamaz.'
      return
    }
    busy = provider
    errors[provider] = ''
    notices[provider] = ''
    try {
      await api.setAdminKey(provider, key)
      drafts[provider] = '' // do not keep the key in the UI
      notices[provider] = 'Anahtar işletim sisteminin anahtar deposuna kaydedildi.'
      await reload()
      void refreshMeta()
    } catch (e) {
      errors[provider] = errorMessage(e)
    } finally {
      busy = ''
    }
  }

  async function remove(provider: string) {
    busy = provider
    errors[provider] = ''
    notices[provider] = ''
    try {
      await api.deleteAdminKey(provider)
      notices[provider] = 'Anahtar silindi; bu kaynak artık taranmaz.'
      await reload()
      void refreshMeta()
    } catch (e) {
      errors[provider] = errorMessage(e)
    } finally {
      busy = ''
    }
  }
</script>

<div class="view">
  {#if $keys.status === 'error'}
    <section class="card state error" role="alert">
      <h2>Anahtar durumu okunamadı</h2>
      <p>{$keys.error}</p>
      <button type="button" class="btn" onclick={reload}>Yeniden dene</button>
    </section>
  {:else if !$keys.data}
    <p class="muted" aria-busy="true">Yükleniyor…</p>
  {:else}
    <div class="callout">
      <span aria-hidden="true">●</span>
      <div class="body">
        <p>
          Admin key'ler kuruluşunuzun kullanım raporlarını okumak içindir; sıradan API anahtarı değildir. Anahtar yalnız
          işletim sisteminin anahtar deposunda saklanır: veritabanına, dışa aktarmalara ve günlüklere yazılmaz, bu ekrana
          da geri gönderilmez (yalnız son 4 karakteri görünür).
        </p>
      </div>
    </div>

    {#if $keys.data.keychainError}
      <div class="callout error" role="alert">
        <span aria-hidden="true">■</span>
        <div class="body">
          <p><strong>Anahtar deposu kullanılamıyor.</strong></p>
          <p>{$keys.data.keychainError}</p>
          <p>API kaynakları anahtar olmadan taranamaz; yerel kaynaklar (Claude Code, Codex CLI, Gemini CLI) etkilenmez.</p>
        </div>
      </div>
    {/if}

    <ul class="list">
      {#each $keys.data.keys as k (k.provider)}
        {@const help = HELP[k.provider]}
        {@const unavailable = !!$keys.data.keychainError}
        <li class="card">
          <div class="head">
            <h3>{k.label}</h3>
            {#if k.configured}
              <span class="pill on">Kayıtlı <span class="mono">{k.masked}</span></span>
            {:else}
              <span class="pill">Kayıtlı değil</span>
            {/if}
          </div>
          {#if help}
            <p class="muted small">Nereden alınır: {help.where}</p>
          {/if}

          <form
            onsubmit={(e) => {
              e.preventDefault()
              void save(k.provider)
            }}
          >
            <div class="field">
              <label for="key-{k.provider}">{k.configured ? 'Anahtarı değiştir' : 'Admin key'}</label>
              <input
                id="key-{k.provider}"
                type="password"
                autocomplete="off"
                autocapitalize="off"
                spellcheck="false"
                placeholder={help?.placeholder}
                disabled={unavailable || busy === k.provider}
                aria-invalid={errors[k.provider] ? 'true' : undefined}
                aria-describedby={errors[k.provider] ? `key-msg-${k.provider}` : undefined}
                bind:value={drafts[k.provider]}
              />
            </div>
            <div class="actions">
              <button type="submit" class="btn btn-primary" disabled={unavailable || busy === k.provider || !(drafts[k.provider] ?? '').trim()}>
                Kaydet
              </button>
              {#if k.configured}
                <ConfirmButton label="Sil" target="{k.label} anahtarı" disabled={busy === k.provider} onconfirm={() => remove(k.provider)} />
              {/if}
            </div>
          </form>

          <div id="key-msg-{k.provider}" aria-live="polite">
            {#if errors[k.provider]}
              <p class="callout error" role="alert"><span aria-hidden="true">■</span><span class="body">{errors[k.provider]}</span></p>
            {:else if k.error && !unavailable}
              <p class="callout error"><span aria-hidden="true">■</span><span class="body">{k.error}</span></p>
            {:else if notices[k.provider]}
              <p class="callout ok"><span aria-hidden="true">●</span><span class="body">{notices[k.provider]}</span></p>
            {/if}
          </div>
        </li>
      {/each}
    </ul>
    <p class="muted small">
      Kaydedilen anahtarla veri çekmek için üstteki "Yeniden tara" düğmesini kullanın.
    </p>
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
  li {
    display: grid;
    gap: 10px;
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
  form {
    display: grid;
    gap: 10px;
    max-width: 560px;
  }
  .small {
    font-size: 0.82rem;
  }
</style>
