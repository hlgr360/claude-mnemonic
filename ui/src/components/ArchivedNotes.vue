<script setup lang="ts">
import { onMounted, ref, watch } from 'vue'
import type { Observation } from '@/types'
import { archivedReasonText, describeFoldError, listArchived, unarchiveNote } from '@/utils/folds'
import { formatRelativeTime } from '@/utils/formatters'

const props = defineProps<{
  /** The project filter of the dashboard; null shows every project. */
  project: string | null
}>()

const emit = defineEmits<{
  /** A note was put back, so the notes elsewhere are stale. */
  changed: []
}>()

const PAGE = 50

const items = ref<Observation[]>([])
const total = ref(0)
const loading = ref(false)
const working = ref<number | null>(null)
const error = ref('')
let loadSeq = 0

async function load(append = false) {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await listArchived({ project: props.project, limit: PAGE, offset: append ? items.value.length : 0 })
    if (seq !== loadSeq) return
    items.value = append ? [...items.value, ...res.observations] : res.observations
    total.value = res.total
    error.value = ''
  } catch (err) {
    if (seq === loadSeq) error.value = describeFoldError(err)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function restore(o: Observation) {
  if (working.value !== null) return
  working.value = o.id
  try {
    await unarchiveNote(o.id)
    items.value = items.value.filter(x => x.id !== o.id)
    total.value = Math.max(0, total.value - 1)
    error.value = ''
    emit('changed')
  } catch (err) {
    error.value = describeFoldError(err)
  } finally {
    working.value = null
  }
}

watch(() => props.project, () => load())
onMounted(() => void load())

const shortProject = (p: string) => p.split('/').pop() ?? p
</script>

<template>
  <div data-testid="archived-notes">
    <p class="text-sm text-slate-300 mb-1">
      Archived notes are kept but hidden from search and sessions. Roll-ups, consolidations, the cap on notes per project and you put notes here.
    </p>
    <p class="text-xs text-slate-500 mb-4">
      Putting one back makes it live again. A roll-up or consolidation can also be undone as a whole from the History view.
    </p>

    <div v-if="error" data-testid="archived-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200">{{ error }}</div>

    <div v-if="!loading && items.length === 0 && !error" data-testid="archived-empty" class="glass rounded-xl p-8 border border-white/10 text-center text-slate-400 text-sm">
      No archived notes{{ project ? ' in this project' : '' }}.
    </div>

    <ul v-else class="space-y-2" data-testid="archived-list">
      <li v-for="o in items" :key="o.id" data-testid="archived-item" class="glass rounded-xl border border-white/10 p-3 flex items-start gap-3">
        <div class="flex-1 min-w-0">
          <div class="text-sm text-slate-100">{{ o.title || 'Untitled' }}</div>
          <div class="text-xs text-slate-500 mt-0.5">
            <span class="font-mono">#{{ o.id }}</span> · {{ o.type }} · saved {{ formatRelativeTime(o.created_at_epoch) }}
            <template v-if="o.project"> · <span class="text-amber-600/80 font-mono">{{ shortProject(o.project) }}</span></template>
          </div>
          <div class="text-xs text-slate-400 mt-1" data-testid="archived-reason">{{ archivedReasonText(o.archived_reason) }}</div>
        </div>
        <button
          data-testid="archived-restore"
          :disabled="working !== null"
          class="px-3 py-1.5 rounded-lg text-xs font-medium bg-white/10 text-slate-200 hover:bg-white/20 disabled:opacity-50 flex-shrink-0"
          @click="restore(o)"
        >
          Put back
        </button>
      </li>
      <li v-if="total > items.length" class="text-center py-1">
        <button data-testid="archived-more" :disabled="loading" class="text-xs text-claude-300 hover:text-claude-200 underline" @click="load(true)">
          Show more ({{ items.length }} of {{ total }})
        </button>
      </li>
    </ul>
  </div>
</template>
