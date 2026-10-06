<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import type { Fold, FoldKind, Observation } from '@/types'
import {
  describeFoldError, foldHeadline, foldKindLabel, getNote, listFolds, notesText, restoreFold, restoreSentence, FoldApiError
} from '@/utils/folds'
import { formatRelativeTime } from '@/utils/formatters'

const props = defineProps<{
  /** The project filter of the dashboard; null shows every project. */
  project: string | null
}>()

const emit = defineEmits<{
  /** A fold was restored, so the notes elsewhere are stale. */
  changed: []
}>()

const items = ref<Fold[]>([])
const selectedId = ref<number | null>(null)
const showRestored = ref(false)
const kind = ref<FoldKind | ''>('')
const loading = ref(false)
const working = ref(false)
const confirming = ref(false)
const error = ref('')
const notice = ref('')
const sourceNotes = ref<Observation[] | null>(null)
const loadingNotes = ref(false)
let loadSeq = 0

const selected = computed(() => items.value.find(f => f.id === selectedId.value) ?? null)
const shortProject = (p: string) => p.split('/').pop() ?? p

async function load(keep: number | null = selectedId.value) {
  const seq = ++loadSeq
  loading.value = true
  try {
    const res = await listFolds({ project: props.project, kind: kind.value || null, includeUndone: showRestored.value, limit: 100 })
    if (seq !== loadSeq) return
    items.value = res.folds
    selectedId.value = keep !== null && res.folds.some(f => f.id === keep) ? keep : (res.folds[0]?.id ?? null)
    error.value = ''
  } catch (err) {
    if (seq === loadSeq) error.value = describeFoldError(err)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

async function showNotes() {
  const f = selected.value
  if (!f || loadingNotes.value) return
  loadingNotes.value = true
  try {
    const notes = await Promise.all(f.sources.slice(0, 50).map(id => getNote(id).catch(() => null)))
    sourceNotes.value = notes.filter((n): n is Observation => n !== null)
  } finally {
    loadingNotes.value = false
  }
}

async function restore() {
  const f = selected.value
  if (!f || f.undone || working.value) return
  working.value = true
  confirming.value = false
  try {
    const report = await restoreFold(f.id)
    notice.value = `${foldKindLabel(f.kind)} restored. ${restoreSentence(report)}`
    error.value = ''
    emit('changed')
    await load(null)
  } catch (err) {
    error.value = describeFoldError(err)
    if (err instanceof FoldApiError && [404, 409].includes(err.status)) await load()
  } finally {
    working.value = false
  }
}

watch(selectedId, () => {
  confirming.value = false
  sourceNotes.value = null
})
watch([() => props.project, showRestored, kind], () => load(null))
onMounted(() => void load(null))

const kindIcon = (k: FoldKind) => (k === 'rollup' ? 'fa-boxes-stacked' : 'fa-code-merge')
</script>

<template>
  <div data-testid="fold-history">
    <div class="flex items-center gap-3 mb-3 flex-wrap text-sm">
      <label class="flex items-center gap-1 text-slate-400">
        Show:
        <select
          v-model="kind"
          data-testid="fold-kind"
          class="bg-white/5 border border-white/10 rounded px-2 py-1 text-xs text-slate-300 focus:outline-none focus:border-claude-500"
        >
          <option value="">Roll-ups and consolidations</option>
          <option value="rollup">Roll-ups</option>
          <option value="consolidation">Consolidations</option>
        </select>
      </label>
      <label class="flex items-center gap-1.5 text-slate-400">
        <input v-model="showRestored" data-testid="fold-show-restored" type="checkbox" class="accent-claude-500">
        Include the ones that were restored
      </label>
    </div>

    <div v-if="notice" data-testid="fold-notice" class="mb-3 px-3 py-2 rounded-lg bg-emerald-500/10 border border-emerald-500/30 text-sm text-emerald-200 flex items-start gap-3" role="status">
      <span class="flex-1">{{ notice }}</span>
      <button class="text-emerald-200/70 hover:text-white" aria-label="Dismiss" @click="notice = ''"><i class="fas fa-times" /></button>
    </div>
    <div v-if="error" data-testid="fold-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200 flex items-center gap-3">
      <span class="flex-1">{{ error }}</span>
      <button class="text-xs underline" @click="load()">Retry</button>
    </div>

    <div v-if="!loading && items.length === 0 && !error" data-testid="fold-empty" class="glass rounded-xl p-8 border border-white/10 text-center text-slate-400">
      <i class="fas fa-boxes-stacked text-2xl text-claude-400/70 mb-3" />
      <p class="font-medium text-slate-200">Nothing has been rolled up or consolidated{{ project ? ' in this project' : '' }} yet.</p>
      <p class="text-sm mt-2 max-w-xl mx-auto">
        Use <strong>Roll up</strong> to condense a project's old notes, or <strong>Duplicates</strong> to fold near-identical notes together.
        Either way the originals are archived, never deleted, and can be restored here.
      </p>
    </div>

    <div v-else-if="items.length > 0" class="flex gap-4 items-start flex-col lg:flex-row">
      <ul class="lg:w-80 w-full flex-shrink-0 space-y-2 lg:max-h-[70vh] overflow-y-auto" data-testid="fold-list">
        <li v-for="f in items" :key="f.id">
          <button
            data-testid="fold-item"
            class="w-full text-left p-3 rounded-lg border transition-colors"
            :class="f.id === selectedId ? 'bg-claude-500/15 border-claude-500/50' : 'bg-white/5 border-white/10 hover:bg-white/10'"
            @click="selectedId = f.id"
          >
            <div class="flex items-center gap-2 mb-1">
              <i class="fas text-xs" :class="[kindIcon(f.kind), f.kind === 'rollup' ? 'text-violet-300' : 'text-sky-300']" />
              <span class="text-xs font-medium text-slate-300">{{ foldKindLabel(f.kind) }}</span>
              <span v-if="f.undone" class="px-1.5 rounded-full bg-slate-500/30 text-[11px] text-slate-200">restored</span>
              <span class="ml-auto text-[11px] text-slate-500">{{ formatRelativeTime(f.created_epoch) }}</span>
            </div>
            <div class="text-sm text-slate-100 line-clamp-2">{{ foldHeadline(f) }}</div>
            <div class="text-[11px] text-amber-600/80 font-mono mt-1">{{ shortProject(f.project) }}</div>
          </button>
        </li>
      </ul>

      <div v-if="selected" data-testid="fold-detail" class="flex-1 min-w-0 glass rounded-xl border border-white/10 p-4 w-full">
        <div class="text-sm font-medium text-slate-100">{{ foldKindLabel(selected.kind) }}: {{ foldHeadline(selected) }}</div>
        <div class="text-xs text-slate-500 mt-1">
          {{ shortProject(selected.project) }} · {{ formatRelativeTime(selected.created_epoch) }}
          <template v-if="selected.undone"> · restored {{ formatRelativeTime(selected.undone_epoch ?? selected.created_epoch) }}</template>
        </div>

        <div class="mt-3 text-sm text-slate-300">
          <template v-if="selected.kind === 'rollup'">The roll-up note</template>
          <template v-else>The note that was kept</template>:
          <strong class="text-amber-100" data-testid="fold-survivor">#{{ selected.survivor }}<template v-if="selected.survivor_title"> {{ selected.survivor_title }}</template></strong>
        </div>

        <div class="mt-3">
          <div class="text-[11px] uppercase tracking-wide text-slate-500 mb-1">{{ selected.kind === 'rollup' ? 'Condensed' : 'Archived' }} ({{ notesText(selected.sources.length) }})</div>
          <div v-if="!sourceNotes" class="flex flex-wrap gap-1.5 items-center">
            <span v-for="id in selected.sources.slice(0, 40)" :key="id" class="px-2 py-0.5 rounded border border-white/10 bg-white/5 text-xs text-slate-300 font-mono">#{{ id }}</span>
            <span v-if="selected.sources.length > 40" class="text-xs text-slate-500">and {{ selected.sources.length - 40 }} more</span>
            <button data-testid="fold-show-notes" class="ml-2 text-xs text-claude-300 hover:text-claude-200 underline" :disabled="loadingNotes" @click="showNotes">
              {{ loadingNotes ? 'Loading…' : 'Show the notes' }}
            </button>
          </div>
          <ul v-else data-testid="fold-source-notes" class="space-y-1 max-h-72 overflow-y-auto">
            <li v-for="n in sourceNotes" :key="n.id" class="text-sm text-slate-300">
              <span class="font-mono text-xs text-slate-500">#{{ n.id }}</span> {{ n.title || 'Untitled' }}
              <span v-if="n.is_archived" class="ml-1 text-[11px] text-slate-500">(archived)</span>
              <span v-else class="ml-1 text-[11px] text-emerald-400/80">(live)</span>
            </li>
          </ul>
        </div>

        <div class="mt-5 pt-4 border-t border-white/10">
          <div v-if="selected.undone" class="text-sm text-slate-400" data-testid="fold-restored">
            This was restored: the notes are live again.
          </div>
          <div v-else-if="!confirming">
            <button
              data-testid="fold-restore"
              :disabled="working"
              class="px-3 py-1.5 rounded-lg text-sm font-medium bg-white/10 text-slate-200 hover:bg-white/20 disabled:opacity-50"
              @click="confirming = true"
            >
              Restore the {{ notesText(selected.sources.length) }}
            </button>
            <p class="text-xs text-slate-500 mt-2">
              <template v-if="selected.kind === 'rollup'">The roll-up note is archived and the notes it condensed are live again.</template>
              <template v-else>The duplicates are live again and the kept note gives back what it took over from them.</template>
            </p>
          </div>
          <div v-else class="flex flex-wrap items-center gap-3 text-sm" data-testid="fold-confirm">
            <span class="text-slate-200">
              Restore {{ notesText(selected.sources.length) }}?
              <template v-if="selected.kind === 'rollup'">The roll-up note will be archived.</template>
            </span>
            <button data-testid="fold-restore-confirm" :disabled="working" class="px-3 py-1.5 rounded-lg bg-claude-500 text-white hover:bg-claude-400 disabled:opacity-50" @click="restore">Yes, restore</button>
            <button class="px-3 py-1.5 rounded-lg text-slate-400 hover:text-white" @click="confirming = false">Cancel</button>
          </div>
        </div>
      </div>
    </div>
  </div>
</template>
