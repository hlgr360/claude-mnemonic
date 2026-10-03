<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import type { ProjectActionResult, ProjectAlias, ProjectSummary } from '@/types'
import {
  deleteProject, describeError, listAliases, listProjects, mergeProject, removeAlias
} from '@/utils/projectAdmin'
import { formatRelativeTime } from '@/utils/formatters'

export interface ProjectChange {
  kind: 'delete' | 'merge' | 'alias'
  project: string
  into?: string
}

const emit = defineEmits<{
  close: []
  changed: [change: ProjectChange]
}>()

// A delete or merge waits here, as a preview, until the person confirms it.
interface Pending {
  kind: 'delete' | 'merge'
  project: string
  into?: string
  preview: ProjectActionResult
}

const projects = ref<ProjectSummary[]>([])
const aliases = ref<ProjectAlias[]>([])
const loading = ref(true)
const working = ref(false)
const error = ref('')
const notice = ref('')
const pending = ref<Pending | null>(null)
const mergeSource = ref<string | null>(null)
const mergeTarget = ref('')

// An alias that still has data cannot be deleted or merged: act on the project it points to.
const isActionable = (p: ProjectSummary) => !p.alias_of

const mergeTargets = computed(() =>
  projects.value.filter(p => isActionable(p) && p.project !== mergeSource.value)
)

async function load() {
  loading.value = true
  try {
    ;[projects.value, aliases.value] = await Promise.all([listProjects(), listAliases()])
    error.value = ''
  } catch (err) {
    error.value = describeError(err)
  } finally {
    loading.value = false
  }
}

onMounted(load)

// Run an action, showing its error instead of throwing.
async function guarded<T>(fn: () => Promise<T>): Promise<T | undefined> {
  working.value = true
  error.value = ''
  notice.value = ''
  try {
    return await fn()
  } catch (err) {
    error.value = describeError(err)
    return undefined
  } finally {
    working.value = false
  }
}

async function previewDelete(p: ProjectSummary) {
  const preview = await guarded(() => deleteProject(p.project))
  if (preview) {
    mergeSource.value = null
    pending.value = { kind: 'delete', project: p.project, preview }
  }
}

function startMerge(p: ProjectSummary) {
  pending.value = null
  mergeSource.value = p.project
  mergeTarget.value = ''
}

async function previewMerge() {
  if (!mergeSource.value || !mergeTarget.value) return
  const source = mergeSource.value
  const into = mergeTarget.value
  const preview = await guarded(() => mergeProject(source, into))
  if (preview) {
    pending.value = { kind: 'merge', project: source, into: preview.into ?? into, preview }
  }
}

function cancel() {
  pending.value = null
  mergeSource.value = null
  error.value = ''
}

async function confirm() {
  const current = pending.value
  const token = current?.preview.confirm
  if (!current || !token) return

  const result = await guarded(() =>
    current.kind === 'delete'
      ? deleteProject(current.project, token)
      : mergeProject(current.project, current.into ?? '', token)
  )
  if (!result) {
    // A stale token means the data changed: show the fresh numbers instead of the old preview.
    if (error.value.includes('changed since the preview')) {
      const refreshed = await guarded(() =>
        current.kind === 'delete'
          ? deleteProject(current.project)
          : mergeProject(current.project, current.into ?? '')
      )
      if (refreshed) {
        pending.value = { ...current, preview: refreshed }
        error.value = 'The project changed since the preview. The numbers below are current; confirm again if they are right.'
      }
    }
    return
  }

  pending.value = null
  mergeSource.value = null
  notice.value = result.message
  emit('changed', { kind: current.kind, project: current.project, into: current.into })
  await load()
}

async function dropAlias(alias: string) {
  const done = await guarded(async () => {
    await removeAlias(alias)
    return true
  })
  if (done) {
    emit('changed', { kind: 'alias', project: alias })
    await load()
  }
}

const pendingTitle = computed(() => {
  const p = pending.value
  if (!p) return ''
  return p.kind === 'delete' ? `Delete ${p.project}?` : `Merge ${p.project} into ${p.into}?`
})
</script>

<template>
  <Teleport to="body">
    <div class="fixed inset-0 z-50 flex items-center justify-center p-4">
      <div class="absolute inset-0 bg-black/60 backdrop-blur-sm" @click="emit('close')" />

      <div class="relative glass border border-white/10 rounded-2xl p-6 max-w-2xl w-full max-h-[85vh] flex flex-col shadow-2xl">
        <button class="absolute top-4 right-4 text-slate-400 hover:text-white" aria-label="Close" @click="emit('close')">
          <i class="fas fa-times" />
        </button>

        <div class="mb-4">
          <h3 class="text-xl font-bold text-white">Manage projects</h3>
          <p class="text-slate-400 text-sm">
            Merge a project into another, or delete it. Every change is previewed first and the database is backed up automatically.
          </p>
        </div>

        <!-- Messages -->
        <div v-if="error" class="mb-3 p-3 rounded-lg bg-red-500/10 border border-red-500/30 text-red-300 text-sm" role="alert">
          <i class="fas fa-exclamation-circle mr-2" />{{ error }}
        </div>
        <div v-if="notice" class="mb-3 p-3 rounded-lg bg-green-500/10 border border-green-500/30 text-green-300 text-sm" role="status">
          <i class="fas fa-check-circle mr-2" />{{ notice }}
        </div>

        <!-- Confirmation: shows exactly what will happen before anything does -->
        <div v-if="pending" class="mb-4 p-4 rounded-xl border-2 border-amber-500/50 bg-amber-500/5">
          <h4 class="text-white font-semibold mb-1">{{ pendingTitle }}</h4>
          <p class="text-slate-300 text-sm mb-3">{{ pending.preview.message.split(' Nothing was changed')[0] }}</p>
          <div class="grid grid-cols-3 gap-2 text-center mb-3">
            <div v-for="[label, value] in [
              ['Observations', pending.preview.stats.observations],
              ['Sessions', pending.preview.stats.sessions],
              ['Summaries', pending.preview.stats.summaries],
              ['Prompts', pending.preview.stats.prompts],
              ['Vectors', pending.preview.stats.vectors],
              ['Archived', pending.preview.stats.archived_observations]
            ]" :key="label as string" class="rounded-lg bg-slate-800/60 py-2">
              <div class="text-white font-semibold">{{ value }}</div>
              <div class="text-slate-500 text-xs">{{ label }}</div>
            </div>
          </div>
          <p v-if="pending.kind === 'merge'" class="text-slate-400 text-xs mb-3">
            Embeddings are kept as they are. {{ pending.project }} will keep resolving to {{ pending.into }}.
          </p>
          <p v-else class="text-slate-400 text-xs mb-3">
            Deleting is permanent, but a backup of the whole database is saved first so it can be restored.
          </p>
          <div class="flex gap-3">
            <button
              class="flex-1 py-2 rounded-lg font-semibold transition-colors disabled:opacity-50"
              :class="pending.kind === 'delete' ? 'bg-red-500 text-white hover:bg-red-400' : 'bg-claude-500 text-slate-900 hover:bg-claude-400'"
              :disabled="working"
              @click="confirm"
            >
              <i v-if="working" class="fas fa-spinner animate-spin mr-2" />
              {{ pending.kind === 'delete' ? 'Delete project' : 'Merge projects' }}
            </button>
            <button class="flex-1 py-2 rounded-lg bg-white/5 text-slate-300 hover:bg-white/10" :disabled="working" @click="cancel">
              Cancel
            </button>
          </div>
        </div>

        <!-- Merge target picker -->
        <div v-else-if="mergeSource" class="mb-4 p-4 rounded-xl border border-slate-700 bg-slate-800/40">
          <p class="text-slate-300 text-sm mb-2">Merge <span class="text-white font-medium">{{ mergeSource }}</span> into:</p>
          <div class="flex gap-3">
            <select v-model="mergeTarget" class="flex-1 bg-slate-900 border border-slate-600 rounded-lg px-3 py-2 text-sm text-slate-200" aria-label="Merge target">
              <option value="" disabled>Choose a project…</option>
              <option v-for="t in mergeTargets" :key="t.project" :value="t.project">{{ t.display_name }} ({{ t.project }})</option>
            </select>
            <button
              class="px-4 py-2 rounded-lg bg-claude-500 text-slate-900 font-semibold hover:bg-claude-400 disabled:opacity-40"
              :disabled="!mergeTarget || working"
              @click="previewMerge"
            >
              Preview
            </button>
            <button class="px-4 py-2 rounded-lg bg-white/5 text-slate-300 hover:bg-white/10" @click="cancel">Cancel</button>
          </div>
        </div>

        <!-- Project list -->
        <div class="overflow-y-auto min-h-0 flex-1 space-y-2">
          <div v-if="loading" class="text-center text-slate-500 text-sm py-6">
            <i class="fas fa-spinner animate-spin mr-2" />Loading projects…
          </div>
          <div v-else-if="projects.length === 0" class="text-center text-slate-500 text-sm py-6">No projects yet.</div>

          <div
            v-for="p in projects"
            v-else
            :key="p.project"
            class="flex items-center gap-3 p-3 rounded-lg border border-slate-700/60 bg-slate-800/30"
          >
            <i class="fas fa-folder text-slate-500" />
            <div class="min-w-0 flex-1">
              <div class="text-slate-200 text-sm font-medium truncate">{{ p.display_name }}</div>
              <div class="text-slate-500 text-xs font-mono truncate">{{ p.project }}</div>
              <div class="text-slate-400 text-xs mt-0.5">
                {{ p.observations }} observations · {{ p.sessions }} sessions<span v-if="p.last_active_epoch"> · active {{ formatRelativeTime(p.last_active_epoch) }}</span>
              </div>
              <div v-if="p.aliases?.length" class="mt-1 flex flex-wrap gap-1">
                <span v-for="a in p.aliases" :key="a" class="px-1.5 py-0.5 rounded bg-slate-700/60 text-slate-300 text-[10px] font-mono">also {{ a }}</span>
              </div>
              <div v-if="p.alias_of" class="text-amber-400/80 text-xs mt-1">Alias of {{ p.alias_of }}: manage that project instead.</div>
            </div>
            <template v-if="isActionable(p)">
              <button
                class="px-2.5 py-1.5 rounded-lg text-xs text-slate-300 bg-white/5 hover:bg-white/10 disabled:opacity-40"
                :disabled="working || projects.length < 2"
                :title="projects.length < 2 ? 'Needs another project to merge into' : 'Merge into another project'"
                :aria-label="`Merge ${p.project}`"
                @click="startMerge(p)"
              >
                <i class="fas fa-code-merge mr-1" />Merge
              </button>
              <button
                class="px-2.5 py-1.5 rounded-lg text-xs text-red-300 bg-red-500/10 hover:bg-red-500/20 disabled:opacity-40"
                :disabled="working"
                :aria-label="`Delete ${p.project}`"
                @click="previewDelete(p)"
              >
                <i class="fas fa-trash mr-1" />Delete
              </button>
            </template>
          </div>

          <!-- Aliases -->
          <div v-if="aliases.length" class="pt-3">
            <p class="text-slate-500 text-xs uppercase tracking-wide mb-2">Aliases</p>
            <div v-for="a in aliases" :key="a.alias" class="flex items-center gap-2 text-xs text-slate-300 py-1">
              <span class="font-mono truncate">{{ a.alias }}</span>
              <i class="fas fa-arrow-right text-slate-600" />
              <span class="font-mono truncate flex-1">{{ a.canonical }}</span>
              <span class="text-slate-500">{{ a.source }}</span>
              <button class="text-slate-500 hover:text-red-300" :aria-label="`Remove alias ${a.alias}`" :disabled="working" @click="dropAlias(a.alias)">
                <i class="fas fa-times" />
              </button>
            </div>
          </div>
        </div>
      </div>
    </div>
  </Teleport>
</template>
