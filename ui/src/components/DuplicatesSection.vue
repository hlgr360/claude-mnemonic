<script setup lang="ts">
import { ref, watch } from 'vue'
import type { ConsolidationPlan, DuplicateGroup } from '@/types'
import {
  applyConsolidation, consolidationAdds, consolidationSummary, describeFoldError, findDuplicateGroups, notesText,
  previewConsolidation, similarityPercent, FoldApiError
} from '@/utils/folds'

const props = defineProps<{
  /** The project to look in; null means none is chosen. */
  project: string | null
}>()

const emit = defineEmits<{
  /** Notes were archived, so the notes elsewhere are stale. */
  changed: []
}>()

const THRESHOLDS = [0.95, 0.9, 0.85, 0.8]

const threshold = ref(0.85)
const groups = ref<DuplicateGroup[] | null>(null)
const checked = ref(0)
const loading = ref(false)
const working = ref(false)
const error = ref('')
const notice = ref('')
// The group being reviewed, with the plan the worker made for it.
const reviewing = ref<number | null>(null)
const plan = ref<ConsolidationPlan | null>(null)

const idsOf = (g: DuplicateGroup) => g.observations.map(o => o.id)

async function find() {
  if (!props.project || loading.value) return
  loading.value = true
  plan.value = null
  reviewing.value = null
  try {
    const res = await findDuplicateGroups(props.project, threshold.value)
    groups.value = res.duplicate_groups ?? []
    checked.value = res.total_checked ?? 0
    error.value = ''
  } catch (err) {
    error.value = describeFoldError(err)
  } finally {
    loading.value = false
  }
}

async function review(index: number) {
  if (!groups.value || working.value) return
  working.value = true
  try {
    plan.value = await previewConsolidation(idsOf(groups.value[index]))
    reviewing.value = index
    error.value = ''
  } catch (err) {
    error.value = describeFoldError(err)
    plan.value = null
    reviewing.value = null
  } finally {
    working.value = false
  }
}

async function apply() {
  if (!groups.value || reviewing.value === null || !plan.value || working.value) return
  const index = reviewing.value
  working.value = true
  try {
    const done = await applyConsolidation(idsOf(groups.value[index]), plan.value.token)
    notice.value = `Consolidated: ${consolidationSummary(done)} They can be restored from the History view.`
    groups.value = groups.value.filter((_, i) => i !== index)
    plan.value = null
    reviewing.value = null
    error.value = ''
    emit('changed')
  } catch (err) {
    error.value = describeFoldError(err)
    if (err instanceof FoldApiError && err.status === 409) {
      // The notes changed since the preview: show the plan as it is now.
      await review(index)
    }
  } finally {
    working.value = false
  }
}

function cancel() {
  plan.value = null
  reviewing.value = null
}

watch(() => props.project, () => {
  groups.value = null
  plan.value = null
  reviewing.value = null
  notice.value = ''
  error.value = ''
})
</script>

<template>
  <div data-testid="duplicates-section">
    <p class="text-sm text-slate-300 mb-1">
      <strong>Consolidating</strong> folds near-identical notes into one: the best of them is kept, takes over what the others had
      (facts, concepts, files, relations), and the others are <strong>archived</strong>, kept and restorable. No model is used.
    </p>
    <p class="text-xs text-slate-500 mb-4">You see exactly what will happen first, and nothing changes until you confirm.</p>

    <div v-if="!project" data-testid="duplicates-no-project" class="glass rounded-xl p-6 border border-white/10 text-center text-slate-400 text-sm">
      Choose a project in the sidebar to look for duplicate notes.
    </div>

    <template v-else>
      <div v-if="notice" data-testid="duplicates-notice" class="mb-3 px-3 py-2 rounded-lg bg-emerald-500/10 border border-emerald-500/30 text-sm text-emerald-200 flex items-start gap-3" role="status">
        <span class="flex-1">{{ notice }}</span>
        <button class="text-emerald-200/70 hover:text-white" aria-label="Dismiss" @click="notice = ''"><i class="fas fa-times" /></button>
      </div>
      <div v-if="error" data-testid="duplicates-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200">{{ error }}</div>

      <div class="flex items-center gap-3 flex-wrap">
        <label class="flex items-center gap-1 text-sm text-slate-400">
          At least
          <select
            v-model.number="threshold"
            data-testid="duplicates-threshold"
            class="bg-white/5 border border-white/10 rounded px-2 py-1 text-xs text-slate-300 focus:outline-none focus:border-claude-500"
          >
            <option v-for="t in THRESHOLDS" :key="t" :value="t">{{ similarityPercent(t) }}</option>
          </select>
          alike
        </label>
        <button
          data-testid="duplicates-find"
          :disabled="loading"
          class="px-3 py-1.5 rounded-lg text-sm font-medium bg-claude-500 text-white hover:bg-claude-400 disabled:opacity-50"
          @click="find"
        >
          {{ loading ? 'Looking…' : 'Find duplicates' }}
        </button>
      </div>

      <p v-if="groups && groups.length === 0" data-testid="duplicates-none" class="mt-4 text-sm text-slate-300">
        No near-duplicates among the newest {{ notesText(checked) }} of this project.
      </p>

      <ul v-if="groups && groups.length > 0" class="mt-4 space-y-3" data-testid="duplicates-list">
        <li v-for="(g, i) in groups" :key="idsOf(g).join('-')" data-testid="duplicates-group" class="glass rounded-xl border border-white/10 p-3">
          <div class="flex items-center gap-2 mb-2 text-xs text-slate-400">
            <span>{{ notesText(g.observations.length) }}, {{ similarityPercent(g.similarity) }} alike or more</span>
            <button
              v-if="reviewing !== i"
              data-testid="duplicates-review"
              :disabled="working"
              class="ml-auto px-2.5 py-1 rounded-lg text-xs font-medium bg-white/10 text-slate-200 hover:bg-white/20 disabled:opacity-50"
              @click="review(i)"
            >
              Review
            </button>
          </div>
          <ul class="space-y-1">
            <li v-for="o in g.observations" :key="o.id" class="text-sm text-slate-200">
              <span class="font-mono text-xs text-slate-500">#{{ o.id }}</span> {{ o.title || 'Untitled' }}
              <span class="text-[11px] text-slate-500 ml-1">{{ o.type }}</span>
            </li>
          </ul>

          <div v-if="reviewing === i && plan" data-testid="duplicates-plan" class="mt-3 pt-3 border-t border-white/10">
            <p class="text-sm text-slate-100" data-testid="duplicates-summary">{{ consolidationSummary(plan) }}</p>
            <p v-if="consolidationAdds(plan).length > 0" class="text-xs text-slate-400 mt-1">
              The kept note takes over {{ consolidationAdds(plan).join(', ') }} from the others.
            </p>
            <p v-if="plan.duplicates.some(d => d.protected)" data-testid="duplicates-protected" class="text-xs text-amber-300 mt-2">
              {{ plan.duplicates.filter(d => d.protected).length === 1 ? 'One of these' : `${plan.duplicates.filter(d => d.protected).length} of these` }}
              is a decision, a rated note or was saved on purpose. Consolidating archives it too. It can be restored.
            </p>
            <p v-if="plan.survivor.protected" class="text-xs text-slate-500 mt-1">The kept note is a protected one, so it is the one that stays.</p>
            <div class="flex items-center gap-3 mt-3">
              <button
                data-testid="duplicates-apply"
                :disabled="working"
                class="px-3 py-1.5 rounded-lg text-sm font-medium bg-claude-500 text-white hover:bg-claude-400 disabled:opacity-50"
                @click="apply"
              >
                {{ working ? 'Consolidating…' : 'Consolidate' }}
              </button>
              <button class="px-3 py-1.5 rounded-lg text-sm text-slate-400 hover:text-white" @click="cancel">Cancel</button>
              <span class="text-xs text-slate-500">A copy of the database is taken first.</span>
            </div>
          </div>
        </li>
      </ul>
    </template>
  </div>
</template>
