<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { applyScope, describePlan, fetchScopePreview, scopeLabel, type ScopePlan } from '@/utils/scope'

const emit = defineEmits<{
  close: []
  /** Scopes were changed, so the notes on screen are stale. */
  applied: []
}>()

const plan = ref<ScopePlan | null>(null)
const result = ref<ScopePlan | null>(null)
const loading = ref(true)
const working = ref(false)
const error = ref('')

const changes = computed(() => (plan.value ? plan.value.to_project + plan.value.to_global : 0))

async function load() {
  loading.value = true
  error.value = ''
  try {
    plan.value = await fetchScopePreview()
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  } finally {
    loading.value = false
  }
}

async function apply() {
  if (!plan.value?.confirm || working.value) return
  working.value = true
  error.value = ''
  try {
    result.value = await applyScope(plan.value.confirm)
    emit('applied')
    plan.value = null
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
    // A stale token means the archive changed since the preview: look again.
    if ((err as { status?: number }).status === 409) await load()
  } finally {
    working.value = false
  }
}

onMounted(load)
</script>

<template>
  <Teleport to="body">
    <div class="fixed inset-0 z-50 flex items-center justify-center p-4" data-testid="scope-review">
      <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="emit('close')" />
      <div class="relative glass border border-white/10 rounded-2xl p-6 max-w-2xl w-full max-h-[85vh] flex flex-col shadow-2xl">
        <button class="absolute top-4 right-4 text-slate-400 hover:text-white" aria-label="Close" @click="emit('close')">
          <i class="fas fa-times" />
        </button>

        <h3 class="text-xl font-bold text-white mb-1">Review scopes</h3>
        <p class="text-slate-400 text-sm mb-4">
          A <strong class="text-slate-200">global</strong> note is shown to sessions of every project; a
          <strong class="text-slate-200">project</strong> note only to its own. Notes are global only when they are tagged as a general
          lesson (best practice or anti-pattern) and changed none of the project's files. Notes whose scope you chose are never changed.
        </p>

        <div v-if="error" class="mb-3 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-300 text-sm" role="alert" data-testid="scope-error">
          {{ error }}
        </div>

        <div v-if="loading" class="text-slate-400 text-sm py-6 text-center"><i class="fas fa-spinner animate-spin mr-2" />Looking at your notes…</div>

        <!-- Done -->
        <div v-else-if="result" data-testid="scope-done" class="p-4 rounded-xl border border-emerald-500/30 bg-emerald-500/10 text-sm text-emerald-100">
          <p class="font-medium mb-1">Changed the scope of {{ result.changed }} notes.</p>
          <p class="text-emerald-200/80">A backup of the database was saved first: <code class="break-all">{{ result.backup }}</code></p>
        </div>

        <!-- Preview -->
        <template v-else-if="plan">
          <p class="text-slate-200 text-sm mb-3" data-testid="scope-summary">{{ describePlan(plan) }}</p>
          <div class="grid grid-cols-4 gap-2 text-center mb-4 text-sm">
            <div v-for="[label, value] in [['Notes', plan.total], ['To project', plan.to_project], ['To global', plan.to_global], ['Kept by hand', plan.protected]]" :key="label as string" class="rounded-lg bg-slate-800/60 py-2">
              <div class="text-white font-semibold">{{ value }}</div>
              <div class="text-slate-500 text-xs">{{ label }}</div>
            </div>
          </div>
          <ul v-if="plan.sample.length" class="overflow-y-auto min-h-0 flex-1 space-y-1 mb-4 max-h-64" data-testid="scope-sample">
            <li v-for="c in plan.sample" :key="c.id" class="flex items-center gap-2 text-xs p-2 rounded bg-white/5">
              <span class="text-slate-500 w-12 flex-shrink-0">{{ scopeLabel(c.from) }}</span>
              <i class="fas fa-arrow-right text-slate-600" />
              <span class="text-claude-300 w-12 flex-shrink-0">{{ scopeLabel(c.to) }}</span>
              <span class="text-slate-200 truncate">{{ c.title || 'Untitled' }}</span>
            </li>
            <li v-if="changes > plan.sample.length" class="text-xs text-slate-500 text-center">and {{ changes - plan.sample.length }} more</li>
          </ul>
          <div class="flex gap-3">
            <button
              data-testid="scope-apply"
              class="flex-1 py-2 rounded-lg font-semibold bg-claude-500 text-slate-900 hover:bg-claude-400 disabled:opacity-40"
              :disabled="changes === 0 || working"
              @click="apply"
            >
              <i v-if="working" class="fas fa-spinner animate-spin mr-2" />Change {{ changes }} scopes
            </button>
            <button class="flex-1 py-2 rounded-lg bg-white/5 text-slate-300 hover:bg-white/10" @click="emit('close')">Cancel</button>
          </div>
          <p class="text-slate-500 text-xs mt-3">The database is backed up before anything changes, and you can change a single note's scope on its card at any time.</p>
        </template>
      </div>
    </div>
  </Teleport>
</template>
