<script lang="ts" module>
  import type { ECharts } from 'echarts/core'

  // ECharts is large: it is loaded on first use, as its own chunk, and only
  // with the parts the dashboard needs.
  let echartsPromise: Promise<typeof import('echarts/core')> | null = null

  function loadEcharts() {
    echartsPromise ??= (async () => {
      const [core, { BarChart }, { GridComponent, TooltipComponent, LegendComponent }, { CanvasRenderer }] =
        await Promise.all([
          import('echarts/core'),
          import('echarts/charts'),
          import('echarts/components'),
          import('echarts/renderers'),
        ])
      core.use([BarChart, GridComponent, TooltipComponent, LegendComponent, CanvasRenderer])
      return core
    })()
    return echartsPromise
  }
</script>

<script lang="ts">
  import { onMount } from 'svelte'
  import { themeTick } from '../lib/stores'

  interface Props {
    /** Builds the ECharts option; re-run when its reactive inputs or the theme change. */
    option: () => Record<string, unknown>
    height?: number
    /** Accessible description of what the chart shows. */
    label: string
  }
  let { option, height = 280, label }: Props = $props()

  let el: HTMLDivElement
  let chart: ECharts | null = null
  let ready = $state(false)
  let failed = $state(false)

  onMount(() => {
    let disposed = false
    let observer: ResizeObserver | undefined
    loadEcharts()
      .then((echarts) => {
        if (disposed) return
        chart = echarts.init(el, undefined, { renderer: 'canvas' })
        observer = new ResizeObserver(() => chart?.resize())
        observer.observe(el)
        ready = true
      })
      .catch(() => {
        failed = true
      })
    return () => {
      disposed = true
      observer?.disconnect()
      chart?.dispose()
      chart = null
    }
  })

  $effect(() => {
    void $themeTick
    const opt = option()
    if (ready && chart) chart.setOption(opt, { notMerge: true })
  })
</script>

<div class="chart" style:height="{height}px" role="img" aria-label={label} bind:this={el}>
  {#if failed}
    <p class="fail">Grafik yüklenemedi; verileri tablo görünümünden okuyabilirsiniz.</p>
  {/if}
</div>

<style>
  .chart {
    width: 100%;
    min-width: 0;
    background: transparent;
  }
  .fail {
    padding: 16px 0;
    font-size: 13px;
    color: var(--critical-text);
  }
</style>
