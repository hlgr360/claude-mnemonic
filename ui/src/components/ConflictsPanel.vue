<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import type { Conflict, ConflictStatus } from '@/types'
import type { Segment } from '@/utils/conflictDiff'
import { buildRows } from '@/utils/conflictDiff'
import {
  DECISIONS, conflictTitle, decisionForKey, decisionLabel, hiddenSide, moveSelection, nextSelection,
  relationLabel, relationShort, resolutionSummary, restorableText, type DecisionOption
} from '@/utils/conflictText'
import { describeConflictError, listConflicts, resolveConflict, undoConflict, ConflictApiError } from '@/utils/conflicts'
import { formatRelativeTime } from '@/utils/formatters'
import { useSSE } from '@/composables/useSSE'

const props = defineProps<{
  /** The project filter of the dashboard; null shows every project. */
  project: string | null
}>()

const emit = defineEmits<{
  /** A decision was made or taken back, so counts elsewhere are stale. */
  changed: []
}>()

const PAGE = 100
const TOAST_MS = 10_000

const status = ref<ConflictStatus>('open')
const items = ref<Conflict[]>([])
const total = ref(0)
const openCount = ref(0)
const selectedId = ref<number | null>(null)
const loading = ref(false)
const working = ref(false)
const error = ref('')
const toast = ref<{ id: number; text: string } | null>(null)
let toastTimer: number | null = null
let loadSeq = 0

const selected = computed(() => items.value.find(c => c.id === selectedId.value) ?? null)
const rows = computed(() => (selected.value ? buildRows(selected.value.older, selected.value.newer) : []))
const ids = computed(() => items.value.map(c => c.id))
const hidden = computed(() => (selected.value ? hiddenSide(selected.value) : null))

async function load(keep: number | null = selectedId.value) {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await listConflicts({ status: status.value, project: props.project, limit: PAGE })
    if (seq !== loadSeq) return // a newer request is on its way
    items.value = res.conflicts
    total.value = res.total
    openCount.value = res.open_count
    selectedId.value = keep !== null && res.conflicts.some(c => c.id === keep) ? keep : (res.conflicts[0]?.id ?? null)
    error.value = ''
  } catch (err) {
    if (seq === loadSeq) error.value = describeConflictError(err)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function showToast(id: number, text: string) {
  if (toastTimer) window.clearTimeout(toastTimer)
  toast.value = { id, text }
  toastTimer = window.setTimeout(() => (toast.value = null), TOAST_MS)
}

function dismissToast() {
  if (toastTimer) window.clearTimeout(toastTimer)
  toastTimer = null
  toast.value = null
}

/** After a failed action: a proposal that is gone or already decided means the list is out of date. */
async function failed(err: unknown) {
  error.value = describeConflictError(err)
  if (err instanceof ConflictApiError && [404, 409, 410].includes(err.status)) await load()
}

async function decide(option: DecisionOption) {
  const c = selected.value
  if (!c || c.resolved || working.value) return
  working.value = true
  try {
    const before = ids.value
    await resolveConflict(c.id, option.decision)
    items.value = items.value.filter(x => x.id !== c.id)
    total.value = Math.max(0, total.value - 1)
    openCount.value = Math.max(0, openCount.value - 1)
    selectedId.value = nextSelection(before, c.id)
    error.value = ''
    showToast(c.id, `${option.label}: ${conflictTitle(c)}`)
    emit('changed')
  } catch (err) {
    await failed(err)
  } finally {
    working.value = false
  }
}

async function undo(id: number) {
  if (working.value) return
  working.value = true
  try {
    const before = ids.value
    await undoConflict(id)
    if (toast.value?.id === id) dismissToast()
    emit('changed')
    // On the resolved list the proposal leaves; on the open list it comes back and is selected.
    await load(status.value === 'resolved' ? nextSelection(before, id) : id)
  } catch (err) {
    await failed(err)
  } finally {
    working.value = false
  }
}

function skip() {
  selectedId.value = moveSelection(ids.value, selectedId.value, 1)
}

function setStatus(next: ConflictStatus) {
  if (status.value === next) return
  status.value = next
  selectedId.value = null
}

function onKey(event: KeyboardEvent) {
  const el = event.target as HTMLElement | null
  if (event.metaKey || event.ctrlKey || event.altKey) return
  if (el && (el.tagName === 'INPUT' || el.tagName === 'SELECT' || el.tagName === 'TEXTAREA' || el.isContentEditable)) return
  if (event.key === 'j' || event.key === 'ArrowDown') {
    selectedId.value = moveSelection(ids.value, selectedId.value, 1)
  } else if (event.key === 'k' || event.key === 'ArrowUp') {
    selectedId.value = moveSelection(ids.value, selectedId.value, -1)
  } else if (event.key === 's') {
    skip()
  } else if (status.value === 'open') {
    const option = decisionForKey(event.key)
    if (option) void decide(option)
  } else {
    return
  }
  event.preventDefault()
}

// Reload on a new project filter or status, and when the worker announces a change from elsewhere.
watch([status, () => props.project], () => load(null))
const { lastEvent } = useSSE()
watch(lastEvent, (event) => {
  if (event?.type === 'conflict' && !working.value) void load()
})

onMounted(() => {
  void load(null)
  window.addEventListener('keydown', onKey)
})
onBeforeUnmount(() => {
  window.removeEventListener('keydown', onKey)
  if (toastTimer) window.clearTimeout(toastTimer)
})

const confidenceClass = (c: Conflict) =>
  c.confidence === 'high' ? 'bg-emerald-400' : c.confidence === 'medium' ? 'bg-amber-400' : 'bg-slate-500'

const segmentClass = (side: 'older' | 'newer', s: Segment) =>
  s.changed ? (side === 'older' ? 'bg-red-500/25 text-red-100 rounded px-0.5' : 'bg-emerald-500/25 text-emerald-100 rounded px-0.5') : ''

const listClass = (side: 'older' | 'newer', changed: boolean) =>
  changed
    ? side === 'older' ? 'bg-red-500/20 border-red-500/40 text-red-100' : 'bg-emerald-500/20 border-emerald-500/40 text-emerald-100'
    : 'bg-white/5 border-white/10 text-slate-300'

const shortProject = (p: string) => p.split('/').pop() ?? p
</script>

<template>
  <div data-testid="conflicts-panel">
    <!-- Status switch -->
    <div class="flex items-center gap-2 mb-3 flex-wrap">
      <button
        data-testid="conflict-status-open"
        class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors"
        :class="status === 'open' ? 'bg-claude-500 text-white' : 'bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white'"
        @click="setStatus('open')"
      >
        To review ({{ openCount }})
      </button>
      <button
        data-testid="conflict-status-resolved"
        class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors"
        :class="status === 'resolved' ? 'bg-claude-500 text-white' : 'bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white'"
        @click="setStatus('resolved')"
      >
        Decided
      </button>
      <span class="ml-auto text-xs text-slate-500 hidden md:inline">
        <kbd class="px-1 rounded bg-white/10">j</kbd>/<kbd class="px-1 rounded bg-white/10">k</kbd> move ·
        <kbd class="px-1 rounded bg-white/10">1</kbd> <kbd class="px-1 rounded bg-white/10">2</kbd> <kbd class="px-1 rounded bg-white/10">3</kbd> decide ·
        <kbd class="px-1 rounded bg-white/10">s</kbd> skip
      </span>
    </div>

    <div v-if="error" data-testid="conflict-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200 flex items-center gap-3">
      <span class="flex-1">{{ error }}</span>
      <button class="text-xs underline" @click="load()">Retry</button>
    </div>

    <!-- Empty -->
    <div
      v-if="!loading && items.length === 0 && !error"
      data-testid="conflict-empty"
      class="glass rounded-xl p-8 border border-white/10 text-center text-slate-400"
    >
      <i class="fas fa-check-circle text-2xl text-emerald-400/70 mb-3" />
      <p v-if="status === 'open'" class="font-medium text-slate-200">Nothing to review.</p>
      <p v-else class="font-medium text-slate-200">No decisions yet.</p>
      <p v-if="status === 'open'" class="text-sm mt-2 max-w-xl mx-auto">
        Proposals appear here when one note seems to replace another. Turn the proposer on with
        <code class="text-claude-300">CLAUDE_MNEMONIC_CONFLICT_PROPOSALS_ENABLED=true</code>, or propose a pair yourself through the API.
        Nothing is ever hidden without your decision.
      </p>
    </div>

    <div v-else-if="items.length > 0" class="flex gap-4 items-start flex-col lg:flex-row">
      <!-- Queue -->
      <ul class="lg:w-72 w-full flex-shrink-0 space-y-2 lg:max-h-[70vh] overflow-y-auto" data-testid="conflict-list">
        <li v-for="c in items" :key="c.id">
          <button
            data-testid="conflict-item"
            class="w-full text-left p-3 rounded-lg border transition-colors"
            :class="c.id === selectedId ? 'bg-claude-500/15 border-claude-500/50' : 'bg-white/5 border-white/10 hover:bg-white/10'"
            @click="selectedId = c.id"
          >
            <div class="flex items-center gap-2 mb-1">
              <span class="w-2 h-2 rounded-full flex-shrink-0" :class="confidenceClass(c)" :title="`Confidence: ${c.confidence || 'unknown'}`" />
              <span class="text-xs font-medium text-slate-300">{{ relationShort(c.relation) }}</span>
              <span class="ml-auto text-[11px] text-slate-500">{{ formatRelativeTime(c.detected_at_epoch) }}</span>
            </div>
            <div class="text-sm text-slate-100 line-clamp-2">{{ conflictTitle(c) }}</div>
            <div class="text-[11px] text-amber-600/80 font-mono mt-1">{{ shortProject(c.newer.project) }}</div>
          </button>
        </li>
        <li v-if="total > items.length" class="text-xs text-slate-500 text-center py-1">
          Showing {{ items.length }} of {{ total }}
        </li>
      </ul>

      <!-- Detail -->
      <div v-if="selected" data-testid="conflict-detail" class="flex-1 min-w-0 glass rounded-xl border border-white/10 p-4 w-full">
        <div class="mb-3">
          <div class="text-sm font-medium text-slate-100">{{ relationLabel(selected.relation) }}</div>
          <p v-if="selected.reason" class="text-sm text-slate-300 mt-1" data-testid="conflict-reason">{{ selected.reason }}</p>
          <div class="text-xs text-slate-500 mt-1">
            {{ selected.proposer === 'manual' ? 'Proposed by you' : 'Proposed by the model' }}<template v-if="selected.confidence"> · confidence {{ selected.confidence }}</template>
          </div>
        </div>

        <!-- Side by side -->
        <div class="grid grid-cols-2 gap-x-4 gap-y-1 text-sm">
          <div class="pb-1 border-b border-red-500/30 text-xs uppercase tracking-wide text-red-300 flex items-center gap-2">
            Older · #{{ selected.older.id }}
            <span v-if="hidden === 'older'" class="normal-case px-1.5 rounded-full bg-slate-500/30 text-slate-200" data-testid="conflict-hidden-older">hidden</span>
          </div>
          <div class="pb-1 border-b border-emerald-500/30 text-xs uppercase tracking-wide text-emerald-300 flex items-center gap-2">
            Newer · #{{ selected.newer.id }}
            <span v-if="hidden === 'newer'" class="normal-case px-1.5 rounded-full bg-slate-500/30 text-slate-200" data-testid="conflict-hidden-newer">hidden</span>
          </div>

          <template v-for="row in rows" :key="row.key">
            <div class="col-span-2 mt-2 text-[11px] uppercase tracking-wide text-slate-500">
              {{ row.label }}<span v-if="row.same" class="ml-2 normal-case text-slate-600">same</span>
            </div>
            <template v-if="row.kind === 'text'">
              <div class="text-slate-200 whitespace-pre-wrap break-words">
                <span v-for="(s, i) in row.left" :key="i" :class="segmentClass('older', s)">{{ s.text }}</span>
              </div>
              <div class="text-slate-200 whitespace-pre-wrap break-words">
                <span v-for="(s, i) in row.right" :key="i" :class="segmentClass('newer', s)">{{ s.text }}</span>
              </div>
            </template>
            <template v-else>
              <div class="flex flex-wrap gap-1.5 content-start">
                <span v-for="(it, i) in row.left" :key="i" class="px-2 py-0.5 rounded border text-xs" :class="listClass('older', it.changed)">{{ it.text }}</span>
              </div>
              <div class="flex flex-wrap gap-1.5 content-start">
                <span v-for="(it, i) in row.right" :key="i" class="px-2 py-0.5 rounded border text-xs" :class="listClass('newer', it.changed)">{{ it.text }}</span>
              </div>
            </template>
          </template>
        </div>

        <!-- Actions -->
        <div class="mt-5 pt-4 border-t border-white/10">
          <div v-if="!selected.resolved" class="flex flex-wrap gap-2 items-center">
            <button
              v-for="d in DECISIONS"
              :key="d.decision"
              :data-testid="`decision-${d.decision}`"
              :title="d.hint"
              :disabled="working"
              class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors disabled:opacity-50"
              :class="d.decision === 'keep_both' ? 'bg-white/10 text-slate-200 hover:bg-white/20' : 'bg-claude-500 text-white hover:bg-claude-400'"
              @click="decide(d)"
            >
              {{ d.label }} <kbd class="ml-1 px-1 rounded bg-black/20 text-[11px]">{{ d.key }}</kbd>
            </button>
            <button data-testid="conflict-skip" class="px-3 py-1.5 rounded-lg text-sm text-slate-400 hover:text-white" @click="skip">
              Skip <kbd class="ml-1 px-1 rounded bg-white/10 text-[11px]">s</kbd>
            </button>
          </div>
          <div v-else class="flex flex-wrap gap-3 items-center text-sm text-slate-300">
            <span data-testid="conflict-decided">
              Decided: <strong>{{ decisionLabel(selected.decision) }}</strong>. {{ resolutionSummary(selected) }}
              {{ restorableText(selected.restorable_until_epoch, Date.now()) }}
            </span>
            <button
              data-testid="conflict-undo"
              :disabled="working"
              class="px-3 py-1.5 rounded-lg text-sm bg-white/10 hover:bg-white/20 disabled:opacity-50"
              @click="undo(selected.id)"
            >
              Undo
            </button>
          </div>
        </div>
      </div>
    </div>

    <!-- Undo toast -->
    <Transition name="toast">
      <div
        v-if="toast"
        data-testid="conflict-toast"
        class="fixed bottom-6 left-1/2 -translate-x-1/2 z-40 flex items-center gap-4 px-4 py-2.5 rounded-xl bg-slate-800 border border-white/15 shadow-xl text-sm text-slate-100"
      >
        <span>{{ toast.text }}</span>
        <button data-testid="conflict-toast-undo" class="font-medium text-claude-300 hover:text-claude-200" @click="undo(toast.id)">Undo</button>
        <button class="text-slate-500 hover:text-slate-300" aria-label="Dismiss" @click="dismissToast">
          <i class="fas fa-times" />
        </button>
      </div>
    </Transition>
  </div>
</template>

<style scoped>
.toast-enter-active,
.toast-leave-active {
  transition: opacity 0.2s ease, transform 0.2s ease;
}

.toast-enter-from,
.toast-leave-to {
  opacity: 0;
  transform: translate(-50%, 8px);
}
</style>
