<script setup lang="ts">
import { ref } from 'vue'
import ArchivedNotes from '@/components/ArchivedNotes.vue'
import DuplicatesSection from '@/components/DuplicatesSection.vue'
import FoldHistory from '@/components/FoldHistory.vue'
import RollupSection from '@/components/RollupSection.vue'

defineProps<{
  /** The project filter of the dashboard; null shows every project. */
  project: string | null
}>()

const emit = defineEmits<{
  /** Notes were archived or put back, so counts and the timeline are stale. */
  changed: []
}>()

type View = 'history' | 'rollup' | 'duplicates' | 'archived'

const VIEWS: { key: View; label: string; icon: string }[] = [
  { key: 'history', label: 'History', icon: 'fa-clock-rotate-left' },
  { key: 'rollup', label: 'Roll up', icon: 'fa-boxes-stacked' },
  { key: 'duplicates', label: 'Duplicates', icon: 'fa-code-merge' },
  { key: 'archived', label: 'Archived notes', icon: 'fa-box-archive' }
]

const view = ref<View>('history')
</script>

<template>
  <div data-testid="folds-panel">
    <div class="flex items-center gap-2 mb-4 flex-wrap" role="tablist">
      <button
        v-for="v in VIEWS"
        :key="v.key"
        role="tab"
        :data-testid="`folds-view-${v.key}`"
        :aria-selected="view === v.key"
        class="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors"
        :class="view === v.key ? 'bg-claude-500 text-white' : 'bg-white/5 text-slate-400 hover:bg-white/10 hover:text-white'"
        @click="view = v.key"
      >
        <i class="fas mr-1.5" :class="v.icon" />{{ v.label }}
      </button>
    </div>

    <FoldHistory v-if="view === 'history'" :project="project" @changed="emit('changed')" />
    <RollupSection v-else-if="view === 'rollup'" :project="project" @changed="emit('changed')" />
    <DuplicatesSection v-else-if="view === 'duplicates'" :project="project" @changed="emit('changed')" />
    <ArchivedNotes v-else :project="project" @changed="emit('changed')" />
  </div>
</template>
