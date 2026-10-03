import { onMounted, onUnmounted, ref, watch, type Ref } from 'vue'
import { fetchTotals, type Totals } from '@/utils/counts'
import { useSSE } from './useSSE'

const REFRESH_DELAY_MS = 500

/**
 * The real totals of observations, prompts and summaries for the project filter. They are null until the
 * first answer, so callers can fall back to what they have. Refreshed when the filter changes and, after a
 * short delay, when the worker announces new or changed data.
 */
export function useTotals(project: Ref<string | null>) {
  const totals = ref<Totals | null>(null)
  const { lastEvent } = useSSE()
  let timer: number | null = null
  let seq = 0

  async function refresh() {
    const mine = ++seq
    try {
      const next = await fetchTotals(project.value)
      if (mine === seq) totals.value = next
    } catch (err) {
      // The numbers are a convenience: keep the last ones rather than show an error for them.
      console.error('[Totals] Failed to read the totals:', err)
    }
  }

  const debouncedRefresh = () => {
    if (timer) window.clearTimeout(timer)
    timer = window.setTimeout(refresh, REFRESH_DELAY_MS)
  }

  watch(project, refresh)
  watch(lastEvent, (event) => {
    if (event && ['observation', 'prompt', 'summary', 'conflict', 'project'].includes(event.type)) debouncedRefresh()
  })
  onMounted(refresh)
  onUnmounted(() => {
    if (timer) window.clearTimeout(timer)
  })

  return { totals, refresh }
}
