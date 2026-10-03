import { onMounted, ref, watch, type Ref } from 'vue'
import { countOpenConflicts } from '@/utils/conflicts'
import { useSSE } from './useSSE'

/**
 * How many conflict proposals wait for a decision, for the badge on the Conflicts tab. It follows the project
 * filter and refreshes when the worker announces a change.
 */
export function useConflictCount(project: Ref<string | null>) {
  const openCount = ref(0)
  const { lastEvent } = useSSE()

  async function refreshCount() {
    try {
      openCount.value = await countOpenConflicts(project.value)
    } catch (err) {
      // The badge is a convenience: keep the last number rather than show an error for it.
      console.error('[Conflicts] Count failed:', err)
    }
  }

  watch(project, refreshCount)
  watch(lastEvent, (event) => {
    if (event?.type === 'conflict') refreshCount()
  })
  onMounted(refreshCount)

  return { openCount, refreshCount }
}
