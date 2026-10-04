<script setup lang="ts">
import { ref, onMounted, watch, computed } from 'vue'
import { fetchSearchAnalytics, fetchRecentSearches, type SearchAnalytics, type RecentQuery } from '@/utils/api'
import { count, percent, timeAgo } from '@/utils/searchAnalytics'
import Card from './Card.vue'

const props = defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  close: []
}>()

const loading = ref(false)
const error = ref<string | null>(null)
const analytics = ref<SearchAnalytics | null>(null)
const recentSearches = ref<RecentQuery[]>([])

const loadData = async () => {
  if (!props.show) return

  loading.value = true
  error.value = null

  try {
    const [analyticsData, searchesData] = await Promise.all([
      fetchSearchAnalytics(),
      fetchRecentSearches(20)
    ])
    analytics.value = analyticsData
    recentSearches.value = searchesData
  } catch (err) {
    error.value = err instanceof Error ? err.message : 'Failed to load search analytics'
  } finally {
    loading.value = false
  }
}

// Load on mount and when show changes
onMounted(() => {
  if (props.show) loadData()
})

watch(() => props.show, (newVal) => {
  if (newVal) loadData()
})

// The kinds of search, most used first
const queryTypes = computed(() =>
  Object.entries(analytics.value?.query_types ?? {}).sort((a, b) => b[1] - a[1])
)
</script>

<template>
  <!-- Modal Backdrop -->
  <Teleport to="body">
    <div
      v-if="show"
      data-testid="search-analytics"
      class="fixed inset-0 z-50 flex items-center justify-center"
    >
      <!-- Backdrop -->
      <div
        class="absolute inset-0 bg-black/60 backdrop-blur-sm"
        @click="emit('close')"
      />

      <!-- Modal Content -->
      <div class="relative w-full max-w-2xl mx-4 max-h-[90vh] overflow-y-auto">
        <Card
          gradient="bg-gradient-to-br from-cyan-500/10 to-blue-500/5"
          border-class="border-cyan-500/30"
        >
          <!-- Header -->
          <div class="flex items-center justify-between mb-4">
            <div class="flex items-center gap-2">
              <i class="fas fa-chart-line text-cyan-400" />
              <h3 class="text-lg font-semibold text-cyan-100">Search Analytics</h3>
            </div>
            <button
              aria-label="Close"
              data-testid="search-analytics-close"
              @click="emit('close')"
              class="p-1.5 text-slate-400 hover:text-slate-200 hover:bg-slate-700/50 rounded-lg transition-colors"
            >
              <i class="fas fa-times" />
            </button>
          </div>

          <!-- Loading State -->
          <div v-if="loading" class="flex items-center justify-center py-8">
            <i class="fas fa-circle-notch fa-spin text-2xl text-cyan-400" />
          </div>

          <!-- Error State -->
          <div v-else-if="error" data-testid="search-analytics-error" class="text-center py-8">
            <i class="fas fa-exclamation-triangle text-2xl text-red-400 mb-2" />
            <p class="text-red-300">{{ error }}</p>
          </div>

          <!-- Content -->
          <div v-else-if="analytics" class="space-y-6">
            <p class="text-xs text-slate-500" data-testid="search-analytics-scope">
              The last {{ count(analytics.total_queries) }} searches served since the worker started (at most 100)<span v-if="analytics.project"> for {{ analytics.project }}</span>.
            </p>

            <!-- Overview Stats Grid -->
            <div class="grid grid-cols-2 md:grid-cols-4 gap-3">
              <div class="p-3 bg-slate-800/50 rounded-lg text-center">
                <div class="text-2xl font-bold text-cyan-300" data-testid="stat-total">{{ count(analytics.total_queries) }}</div>
                <div class="text-xs text-slate-500 uppercase tracking-wide">Total Searches</div>
              </div>

              <div class="p-3 bg-slate-800/50 rounded-lg text-center">
                <div class="text-2xl font-bold text-purple-300" data-testid="stat-vector">{{ count(analytics.vector_searches) }}</div>
                <div class="text-xs text-slate-500 uppercase tracking-wide">Vector Searches</div>
              </div>

              <div class="p-3 bg-slate-800/50 rounded-lg text-center">
                <div class="text-2xl font-bold text-blue-300" data-testid="stat-keyword">{{ count(analytics.keyword_searches) }}</div>
                <div class="text-xs text-slate-500 uppercase tracking-wide">Keyword Searches</div>
              </div>

              <div class="p-3 bg-slate-800/50 rounded-lg text-center">
                <div class="text-2xl font-bold text-green-300" data-testid="stat-average">{{ analytics.avg_results.toFixed(1) }}</div>
                <div class="text-xs text-slate-500 uppercase tracking-wide">Avg Results</div>
              </div>
            </div>

            <!-- Rates -->
            <div class="space-y-3">
              <div class="text-xs text-slate-500 uppercase tracking-wide">How searches went</div>

              <div class="flex items-center justify-between p-3 bg-slate-800/30 rounded-lg">
                <div class="flex items-center gap-2">
                  <i class="fas fa-project-diagram text-purple-400 w-5" />
                  <span class="text-slate-300">Used the vector index</span>
                </div>
                <div class="flex items-center gap-2">
                  <div class="w-24 h-2 bg-slate-700 rounded-full overflow-hidden">
                    <div class="h-full bg-purple-500 transition-all" :style="{ width: `${Math.min(100, Math.max(0, analytics.vector_search_rate))}%` }" />
                  </div>
                  <span class="font-mono text-purple-300 w-16 text-right" data-testid="rate-vector">{{ percent(analytics.vector_search_rate) }}</span>
                </div>
              </div>

              <div class="flex items-center justify-between p-3 bg-slate-800/30 rounded-lg">
                <div class="flex items-center gap-2">
                  <i class="fas fa-ban text-amber-400 w-5" />
                  <span class="text-slate-300">Found nothing</span>
                </div>
                <div class="flex items-center gap-2">
                  <div class="w-24 h-2 bg-slate-700 rounded-full overflow-hidden">
                    <div class="h-full bg-amber-500 transition-all" :style="{ width: `${Math.min(100, Math.max(0, analytics.zero_result_rate))}%` }" />
                  </div>
                  <span class="font-mono text-amber-300 w-16 text-right" data-testid="rate-zero">{{ percent(analytics.zero_result_rate) }}</span>
                </div>
              </div>
            </div>

            <!-- Kinds of search -->
            <div v-if="queryTypes.length" class="space-y-3">
              <div class="text-xs text-slate-500 uppercase tracking-wide">Kinds of search</div>
              <div class="flex flex-wrap gap-2">
                <span
                  v-for="[type, n] in queryTypes"
                  :key="type"
                  data-testid="query-type"
                  class="text-xs text-cyan-300 bg-cyan-500/10 px-2 py-1 rounded"
                >{{ type }} <span class="font-mono text-cyan-500">×{{ count(n) }}</span></span>
              </div>
            </div>

            <!-- Top words -->
            <div v-if="analytics.top_keywords.length" class="space-y-3">
              <div class="text-xs text-slate-500 uppercase tracking-wide">Most searched words</div>
              <div class="flex flex-wrap gap-2">
                <span
                  v-for="k in analytics.top_keywords"
                  :key="k.keyword"
                  data-testid="top-keyword"
                  class="text-xs text-slate-300 bg-slate-700/50 px-2 py-1 rounded"
                >{{ k.keyword }} <span class="font-mono text-slate-500">×{{ count(k.count) }}</span></span>
              </div>
            </div>

            <p v-if="analytics.total_queries === 0" data-testid="search-analytics-empty" class="text-sm text-slate-500 text-center">
              Nothing has been searched since the worker started.
            </p>

            <!-- Recent Searches -->
            <div v-if="recentSearches.length > 0" class="space-y-3">
              <div class="text-xs text-slate-500 uppercase tracking-wide">Recent Searches</div>

              <div class="space-y-2 max-h-48 overflow-y-auto">
                <div
                  v-for="(search, index) in recentSearches"
                  :key="index"
                  data-testid="recent-search"
                  class="flex items-center gap-3 p-2 bg-slate-800/30 rounded-lg text-sm"
                >
                  <i class="fas fa-search text-slate-500 text-xs" />
                  <span class="flex-1 text-slate-300 truncate" :title="search.query">{{ search.query }}</span>
                  <span v-if="search.project" class="text-xs text-amber-600/80 font-mono">{{ search.project.split('/').pop() }}</span>
                  <span v-if="search.type" class="text-xs text-cyan-500 bg-cyan-500/10 px-1.5 py-0.5 rounded">{{ search.type }}</span>
                  <span v-if="search.used_vector" class="text-xs text-purple-400">vector</span>
                  <span class="text-xs text-slate-500 font-mono">{{ search.results }} found</span>
                  <span class="text-xs text-slate-600">{{ timeAgo(search.timestamp) }}</span>
                </div>
              </div>
            </div>
          </div>
        </Card>
      </div>
    </div>
  </Teleport>
</template>
