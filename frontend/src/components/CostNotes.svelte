<script lang="ts">
  import type { Totals } from '../lib/api'

  interface Props {
    /** Every Totals whose cost is shown on the screen. */
    totals?: Totals[]
    /** Models from sources that are not Totals (e.g. limit results). */
    uncertain?: string[]
    unknown?: string[]
    /** Date of the built-in price table (as_of). */
    asOf: string
    /** Extra note, e.g. a limit-specific caveat. */
    extra?: string
  }
  let { totals = [], uncertain: extraUncertain = [], unknown: extraUnknown = [], asOf, extra }: Props = $props()

  const union = (pick: (t: Totals) => string[], more: string[]) => [...new Set([...totals.flatMap(pick), ...more])].sort()
  const hasTotalOnly = $derived(totals.some((t) => t.totalOnlyTokens > 0))
  const uncertain = $derived(union((t) => t.tierUncertainModels, extraUncertain))
  const unknown = $derived(union((t) => t.unknownModels, extraUnknown))
</script>

<div class="notes muted" role="note">
  <p>
    Maliyetler <strong>tahminidir</strong>: uygulamayla gelen fiyat tablosundan hesaplanır
    (fiyat tablosu tarihi: <time datetime={asOf}>{asOf}</time>), sağlayıcı fiyat değiştirirse geçmiş kullanım da yeni fiyatla
    hesaplanır. Abonelik ücretini yansıtmaz.
  </p>
  {#if uncertain.length > 0}
    <p>
      <strong>Alt sınır:</strong>
      {uncertain.join(', ')} için istek sayısı bilinmiyor; uzun-prompt fiyat kademesi uygulanamadı, gerçek maliyet daha
      yüksek olabilir.
    </p>
  {/if}
  {#if hasTotalOnly}
    <p>
      <strong>Bölünmemiş token:</strong>
      Bazı kayıtlar girdi/çıktı ayrımı olmadan, yalnız toplam token olarak kaydedilmiş (ör. güncel Codex CLI günlükleri).
      Bunlar token toplamlarına girer ama fiyatlanamaz: yalnızca bu kayıtlardan oluşan satırların maliyeti
      "hesaplanamadı" görünür. Bu kayıtları da kapsayan toplamlarda ve karışık satırlarda maliyet alt sınırdır (≥): yalnız
      fiyatlanabilen kayıtlar sayılır. "Hesaplanamadı" 0 $ demek değildir.
    </p>
  {/if}
  {#if unknown.length > 0}
    <p><strong>Fiyatı bilinmeyen modeller</strong> maliyete dahil değil: {unknown.join(', ')}.</p>
  {/if}
  {#if extra}<p>{extra}</p>{/if}
</div>

<style>
  .notes {
    font-size: 12px;
    line-height: 1.5;
    color: var(--text-3);
    display: grid;
    gap: 4px;
  }
  strong {
    color: var(--text-2);
    font-weight: 600;
  }
</style>
