<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch, nextTick } from 'vue'
import { Network } from 'vis-network'
import {
  RELATION_COLORS, RELATION_KINDS, TYPE_COLORS, countByKind, edgeWidth, fetchGraph, filterGraph, graphSummary, neighbours,
  nodeSize, rebuildGraph, relationLabel, shortLabel, typeColor, type GraphData, type GraphNodeData
} from '@/utils/graphView'
import { useSSE } from '@/composables/useSSE'

const props = defineProps<{
  /** The project filter of the dashboard; null shows every project. */
  project: string | null
}>()

const container = ref<HTMLElement | null>(null)
const graph = ref<GraphData | null>(null)
const status = ref<{ message?: string; pending?: number } | null>(null)
const loading = ref(false)
const error = ref('')
const kinds = ref<Set<string>>(new Set(RELATION_KINDS))
const minConfidence = ref(0.6)
const selectedId = ref<number | null>(null)
const narrative = ref('')
let network: Network | null = null
let loadSeq = 0
let refreshTimer: number | null = null

const filtered = computed(() => (graph.value ? filterGraph(graph.value, { kinds: kinds.value, minConfidence: minConfidence.value }) : { nodes: [], edges: [] }))
const summary = computed(() => (graph.value ? graphSummary(filtered.value.nodes, filtered.value.edges, graph.value) : ''))
const legend = computed(() => countByKind(filtered.value.edges))
const selected = computed<GraphNodeData | null>(() => filtered.value.nodes.find(n => n.id === selectedId.value) ?? null)
const related = computed(() => (selectedId.value === null ? [] : neighbours(filtered.value.nodes, filtered.value.edges, selectedId.value)))
const hasRelations = computed(() => (graph.value?.edges.length ?? 0) > 0)

async function load() {
  const seq = ++loadSeq
  loading.value = true
  try {
    const [g, s] = await Promise.all([
      fetchGraph(props.project),
      fetch(`/api/graph/stats${props.project ? `?project=${encodeURIComponent(props.project)}` : ''}`, { cache: 'no-store' })
        .then(r => (r.ok ? r.json() : null)).catch(() => null)
    ])
    if (seq !== loadSeq) return
    graph.value = g
    status.value = s
    error.value = ''
    if (selectedId.value !== null && !g.nodes.some(n => n.id === selectedId.value)) selectedId.value = null
  } catch (err) {
    if (seq === loadSeq) error.value = err instanceof Error ? err.message : String(err)
  } finally {
    if (seq === loadSeq) loading.value = false
  }
}

function render() {
  if (network) {
    network.destroy()
    network = null
  }
  if (!container.value || filtered.value.nodes.length === 0) return

  const nodes = filtered.value.nodes.map(n => ({
    id: n.id,
    label: n.degree >= 3 ? shortLabel(n.title, 28) : '',
    title: `${n.title}\n${n.type} · ${n.degree} relation${n.degree === 1 ? '' : 's'}`,
    size: nodeSize(n.degree),
    shape: 'dot' as const,
    color: { background: typeColor(n.type), border: '#0f172a', highlight: { background: typeColor(n.type), border: '#f8fafc' } },
    font: { color: '#e2e8f0', size: 11, strokeWidth: 3, strokeColor: '#0f172a' },
    borderWidth: n.id === selectedId.value ? 4 : 1
  }))
  const edges = filtered.value.edges.map(e => ({
    id: e.id,
    from: e.source,
    to: e.target,
    title: `${relationLabel(e.type)} (${e.confidence.toFixed(2)})${e.reason ? `\n${e.reason}` : ''}`,
    width: edgeWidth(e.confidence),
    color: { color: RELATION_COLORS[e.type] ?? '#64748b', opacity: 0.55, highlight: RELATION_COLORS[e.type] ?? '#64748b' },
    smooth: false as const
  }))

  network = new Network(container.value, { nodes, edges }, {
    physics: {
      solver: 'forceAtlas2Based',
      forceAtlas2Based: { gravitationalConstant: -40, springLength: 90, springConstant: 0.05, avoidOverlap: 0.4 },
      stabilization: { iterations: 200, fit: true }
    },
    interaction: { hover: true, tooltipDelay: 150, navigationButtons: false, zoomView: true, dragView: true },
    layout: { improvedLayout: filtered.value.nodes.length < 150 }
  })
  // Once the layout has settled, stop moving: a still picture is easier to read and costs nothing.
  network.once('stabilizationIterationsDone', () => network?.setOptions({ physics: false }))
  network.on('click', (params: { nodes: Array<number | string> }) => {
    selectedId.value = params.nodes.length ? Number(params.nodes[0]) : null
  })
}

async function loadNarrative(id: number | null) {
  narrative.value = ''
  if (id === null) return
  try {
    const r = await fetch(`/api/observations/${id}`)
    if (!r.ok) return
    const o = await r.json()
    if (selectedId.value === id) narrative.value = (o.subtitle || o.narrative || '').toString()
  } catch {
    // The details are a convenience.
  }
}

function select(id: number) {
  selectedId.value = id
  network?.selectNodes([id])
  network?.focus(id, { scale: 1.2, animation: { duration: 300, easingFunction: 'easeInOutQuad' } })
}

function toggleKind(kind: string) {
  const next = new Set(kinds.value)
  if (next.has(kind)) next.delete(kind)
  else next.add(kind)
  kinds.value = next
}

async function rebuild() {
  if (!window.confirm('Delete every relation and build them again from the notes? This takes a minute.')) return
  try {
    await rebuildGraph()
    graph.value = { nodes: [], edges: [], total_nodes: 0, total_edges: 0, truncated: false }
    status.value = { message: 'Building the graph from existing observations.' }
  } catch (err) {
    error.value = err instanceof Error ? err.message : String(err)
  }
}

watch(() => props.project, () => {
  selectedId.value = null
  void load()
})
watch([filtered], async () => {
  await nextTick()
  render()
})
watch(selectedId, (id) => {
  void loadNarrative(id)
  network?.selectNodes(id === null ? [] : [id])
})

// Relations appear in the background, so follow what the worker announces.
const { lastEvent } = useSSE()
watch(lastEvent, (event) => {
  if (event?.type !== 'graph') return
  if (refreshTimer) window.clearTimeout(refreshTimer)
  refreshTimer = window.setTimeout(load, 1500)
})

onMounted(load)
onBeforeUnmount(() => {
  if (refreshTimer) window.clearTimeout(refreshTimer)
  network?.destroy()
  network = null
})
</script>

<template>
  <div data-testid="graph-view">
    <div v-if="error" data-testid="graph-error" class="mb-3 px-3 py-2 rounded-lg bg-red-500/10 border border-red-500/30 text-sm text-red-200 flex items-center gap-3">
      <span class="flex-1">{{ error }}</span>
      <button class="text-xs underline" @click="load">Retry</button>
    </div>

    <!-- Nothing to draw -->
    <div
      v-if="!loading && !hasRelations && !error"
      data-testid="graph-empty"
      class="glass rounded-xl p-8 border border-white/10 text-center text-slate-400"
    >
      <i class="fas fa-diagram-project text-2xl text-cyan-400/70 mb-3" />
      <p class="font-medium text-slate-200">No relations yet.</p>
      <p class="text-sm mt-2 max-w-xl mx-auto">
        <template v-if="status?.message">{{ status.message }}</template>
        <template v-else>
          Relations link a note to the older notes of its project that read alike. They are made in the background,
          a few minutes after a note is saved.
        </template>
        <template v-if="status?.pending"> {{ status.pending }} notes are still to be looked at.</template>
      </p>
    </div>

    <div v-else-if="hasRelations" class="flex gap-4 items-start flex-col lg:flex-row">
      <div class="flex-1 min-w-0 w-full">
        <!-- Controls -->
        <div class="glass rounded-xl border border-white/10 p-3 mb-3 flex flex-wrap items-center gap-x-5 gap-y-2 text-xs">
          <div class="flex items-center gap-2 flex-wrap" data-testid="graph-kinds">
            <button
              v-for="k in RELATION_KINDS"
              :key="k"
              class="px-2 py-1 rounded-full border transition-opacity"
              :class="kinds.has(k) ? 'opacity-100' : 'opacity-40'"
              :style="{ borderColor: RELATION_COLORS[k], color: RELATION_COLORS[k] }"
              :title="`${relationLabel(k)}: ${legend[k] ?? 0} shown. Click to show or hide`"
              @click="toggleKind(k)"
            >
              {{ relationLabel(k) }} <span class="text-slate-400">{{ legend[k] ?? 0 }}</span>
            </button>
          </div>
          <label class="flex items-center gap-2 text-slate-400">
            Minimum confidence
            <input v-model.number="minConfidence" data-testid="graph-confidence" type="range" min="0.5" max="1" step="0.01" class="w-28">
            <span class="text-slate-200 w-8">{{ minConfidence.toFixed(2) }}</span>
          </label>
          <span class="text-slate-500" data-testid="graph-summary">{{ summary }}</span>
          <button class="ml-auto text-slate-500 hover:text-slate-300" title="Delete every relation and build them again" @click="rebuild">
            <i class="fas fa-rotate" /> Rebuild
          </button>
        </div>

        <div class="relative glass rounded-xl border border-white/10 overflow-hidden">
          <div ref="container" data-testid="graph-canvas" class="w-full" style="height: 68vh; min-height: 420px" />
          <div v-if="filtered.nodes.length === 0" class="absolute inset-0 flex items-center justify-center text-sm text-slate-500">
            No relation passes the filters.
          </div>
          <div class="absolute left-3 bottom-3 flex flex-wrap gap-x-3 gap-y-1 text-[11px] text-slate-400 bg-slate-900/70 rounded px-2 py-1">
            <span v-for="(color, type) in TYPE_COLORS" :key="type" class="flex items-center gap-1">
              <span class="w-2.5 h-2.5 rounded-full inline-block" :style="{ background: color }" /> {{ type }}
            </span>
          </div>
        </div>
      </div>

      <!-- Details -->
      <aside class="lg:w-80 w-full flex-shrink-0 glass rounded-xl border border-white/10 p-4" data-testid="graph-details">
        <div v-if="selected">
          <div class="flex items-center gap-2 mb-1">
            <span class="w-2.5 h-2.5 rounded-full" :style="{ background: typeColor(selected.type) }" />
            <span class="text-xs uppercase tracking-wide text-slate-400">{{ selected.type }}</span>
            <span class="text-[11px] text-amber-600/80 font-mono ml-auto">{{ selected.project.split('/').pop() }}</span>
          </div>
          <h3 class="text-sm font-semibold text-slate-100" data-testid="graph-selected-title">{{ selected.title || 'Untitled' }}</h3>
          <p v-if="narrative" class="text-xs text-slate-400 mt-2 whitespace-pre-wrap">{{ narrative }}</p>
          <div v-if="selected.concepts?.length" class="flex flex-wrap gap-1 mt-2">
            <span v-for="c in selected.concepts" :key="c" class="px-1.5 py-0.5 rounded bg-white/5 text-[11px] text-slate-300">{{ c }}</span>
          </div>
          <div class="mt-3 text-[11px] uppercase tracking-wide text-slate-500">
            {{ related.length }} relation{{ related.length === 1 ? '' : 's' }}
          </div>
          <ul class="mt-1 space-y-1.5 max-h-72 overflow-y-auto">
            <li v-for="r in related" :key="r.edge.id">
              <button class="w-full text-left p-2 rounded bg-white/5 hover:bg-white/10" data-testid="graph-neighbour" @click="select(r.node.id)">
                <div class="text-xs text-slate-200">{{ r.node.title || 'Untitled' }}</div>
                <div class="text-[11px] mt-0.5" :style="{ color: RELATION_COLORS[r.edge.type] }">
                  {{ r.outgoing ? 'relates to older note' : 'a newer note relates to this' }} ·
                  {{ relationLabel(r.edge.type) }} · {{ r.edge.confidence.toFixed(2) }}
                </div>
                <div v-if="r.edge.reason" class="text-[11px] text-slate-500">{{ r.edge.reason }}</div>
              </button>
            </li>
          </ul>
        </div>
        <div v-else class="text-sm text-slate-500">
          <p class="mb-2 text-slate-300">Click a note to see how it is related.</p>
          <p>Bigger circles have more relations. A thicker line is a closer match. Drag to move, scroll to zoom.</p>
        </div>
      </aside>
    </div>
  </div>
</template>
