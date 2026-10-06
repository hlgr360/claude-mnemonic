<script setup lang="ts">
import { ref, watch } from 'vue'
import type { RollupReport } from '@/types'
import { describeFoldError, notesText, previewRollup, rollupGroupLine, rollupOutcome, rollupPressureSentence, runRollup } from '@/utils/folds'

const props = defineProps<{
  /** The project to roll up; null means none is chosen. */
  project: string | null
}>()

const emit = defineEmits<{
  /** Notes were archived and a roll-up written, so the notes elsewhere are stale. */
  changed: []
}>()

const preview = ref<RollupReport | null>(null)
const result = ref<RollupReport | null>(null)
const loading = ref(false)
const running = ref(false)
const error = ref('')

async function showPreview() {
  if (!props.project || loading.value) return
  loading.value = true
  result.value = null
  try {
    preview.value = await previewRollup(props.project)
    error.value = ''
  } catch (err) {
    error.value = describeFoldError(err)
  } finally {
    loading.value = false
  }
}

async function run() {
  if (!props.project || running.value) return
  running.value = true
  try {
    result.value = await runRollup(props.project)
    preview.value = null
    error.value = ''
    if (result.value.groups.some(g => !g.error)) emit('changed')
  } catch (err) {
    error.value = describeFoldError(err)
  } finally {
    running.value = false
  }
}

// A project change is a new question.
watch(() => props.project, () => {
  preview.value = null
  result.value = null
  error.value = ''
})
</script>

<template>
  <div data-testid="rollup-section">
    <p class="text-sm text-slate-300 mb-1">
      A <strong>roll-up</strong> condenses a project's old notes, a month at a time, into one note per group. The originals are
      <strong>archived</strong>: kept, hidden from search and sessions, and restorable. Decisions, rated notes, notes you saved on purpose
      and the newest notes are never rolled up.
    </p>
    <p class="text-sm text-slate-300 mb-1">
      Once a quarter is over, its monthly roll-ups are condensed once more into one <strong>quarter record</strong>. A quarter record is the final
      record: it is kept forever and is never rolled up, consolidated or archived by any rule.
    </p>
    <p class="text-xs text-slate-500 mb-4">A model (Claude, or your local model) writes each roll-up, so this uses some of its usage.</p>

    <div v-if="!project" data-testid="rollup-no-project" class="glass rounded-xl p-6 border border-white/10 text-center text-slate-400 text-sm">
      Choose a project in the sidebar to roll up its old notes.
    </div>

    <template v-else>
      <div v-if="error" data-testid="rollup-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200">{{ error }}</div>

      <button
        data-testid="rollup-preview"
        :disabled="loading || running"
        class="px-3 py-1.5 rounded-lg text-sm font-medium bg-claude-500 text-white hover:bg-claude-400 disabled:opacity-50"
        @click="showPreview"
      >
        {{ loading ? 'Looking…' : 'Show what would be rolled up' }}
      </button>

      <div v-if="preview" data-testid="rollup-preview-result" class="mt-4">
        <p v-if="rollupPressureSentence(preview)" data-testid="rollup-pressure" class="text-xs text-slate-400 mb-2">{{ rollupPressureSentence(preview) }}</p>
        <p v-if="preview.groups.length === 0" data-testid="rollup-nothing" class="text-sm text-slate-300">
          Nothing to roll up: {{ preview.candidates === 0 ? 'no old notes qualify.' : `${notesText(preview.candidates)} qualify, but not enough in one month to be worth a roll-up.` }}
        </p>
        <template v-else>
          <p class="text-sm text-slate-300 mb-2">
            {{ preview.groups.length === 1 ? '1 roll-up' : `${preview.groups.length} roll-ups` }} would be written<template v-if="preview.remaining > 0">
              ({{ preview.remaining }} more groups wait for a later run)</template>:
          </p>
          <ul class="space-y-1.5 mb-4">
            <li v-for="g in preview.groups" :key="g.label" data-testid="rollup-group" class="px-3 py-2 rounded-lg bg-white/5 border border-white/10 text-sm text-slate-200">
              {{ rollupGroupLine(g) }}
            </li>
          </ul>
          <button
            data-testid="rollup-run"
            :disabled="running"
            class="px-3 py-1.5 rounded-lg text-sm font-medium bg-claude-500 text-white hover:bg-claude-400 disabled:opacity-50"
            @click="run"
          >
            {{ running ? 'Writing the roll-ups…' : `Roll up ${preview.groups.length === 1 ? 'this group' : `these ${preview.groups.length} groups`}` }}
          </button>
          <p v-if="running" class="text-xs text-slate-500 mt-2">This can take a few minutes: a model writes each roll-up. A copy of the database is taken first.</p>
        </template>
      </div>

      <div v-if="result" data-testid="rollup-result" class="mt-4">
        <p class="text-sm text-slate-100 mb-2" data-testid="rollup-outcome">{{ rollupOutcome(result) }}</p>
        <ul class="space-y-1.5">
          <li v-for="g in result.groups" :key="g.label" class="px-3 py-2 rounded-lg border text-sm"
              :class="g.error ? 'bg-red-500/10 border-red-500/30 text-red-200' : 'bg-emerald-500/10 border-emerald-500/30 text-emerald-100'">
            {{ rollupGroupLine(g) }}
            <span v-if="g.error"> — {{ g.error }}</span>
            <span v-else> — roll-up #{{ g.rollup_id }}, {{ notesText(g.archived) }} archived</span>
          </li>
        </ul>
        <p v-if="result.groups.some(g => !g.error)" class="text-xs text-slate-500 mt-2">Each one can be restored from the History view.</p>
      </div>
    </template>
  </div>
</template>
