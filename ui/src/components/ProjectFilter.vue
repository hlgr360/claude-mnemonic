<script setup lang="ts">
import { ref, computed, onMounted } from 'vue'
import { listProjects } from '@/utils/projectAdmin'
import { buildProjectOptions, optionMatches, selectedText } from '@/utils/projectOptions'
import type { ProjectOption } from '@/utils/projectOptions'
import ProjectManager from '@/components/ProjectManager.vue'
import type { ProjectChange } from '@/components/ProjectManager.vue'

const props = defineProps<{
  currentProject: string | null
}>()

const emit = defineEmits<{
  'update:project': [project: string | null]
}>()

// The same list the management panel shows: every project that has any data, by name.
const projects = ref<ProjectOption[]>([])
const searchQuery = ref('')
const isOpen = ref(false)
const loading = ref(false)
const showManager = ref(false)

const filteredProjects = computed(() => projects.value.filter(p => optionMatches(p, searchQuery.value)))

const selectedProjectName = computed(() => selectedText(projects.value, props.currentProject))

async function loadProjects() {
  loading.value = true
  try {
    projects.value = buildProjectOptions(await listProjects())
  } catch (err) {
    console.error('[ProjectFilter] Failed to load projects:', err)
  } finally {
    loading.value = false
  }
}

function selectProject(project: string | null) {
  emit('update:project', project)
  isOpen.value = false
  searchQuery.value = ''
}

function toggleDropdown() {
  isOpen.value = !isOpen.value
  if (isOpen.value) {
    // Re-read on each open so a merged or deleted project never reappears (the list is never cached).
    loadProjects()
  }
}

function openManager() {
  isOpen.value = false
  showManager.value = true
}

// After a merge, delete or alias change: re-read the list, and point the filter somewhere that still exists.
async function onProjectsChanged(change: ProjectChange) {
  await loadProjects()
  if (change.kind === 'alias') return
  if (props.currentProject === change.project) {
    // The project we were looking at is gone: follow it into the merge target, or fall back to all projects.
    emit('update:project', change.kind === 'merge' ? change.into ?? null : null)
  } else {
    // Another project changed; refresh what is shown.
    emit('update:project', props.currentProject)
  }
}

// Close dropdown when clicking outside
function handleClickOutside(event: MouseEvent) {
  const target = event.target as HTMLElement
  if (!target.closest('.project-filter')) {
    isOpen.value = false
  }
}

onMounted(() => {
  document.addEventListener('click', handleClickOutside)
  loadProjects()
})
</script>

<template>
  <div class="project-filter relative">
    <!-- Trigger Button -->
    <button
      @click="toggleDropdown"
      class="flex items-center gap-2 px-4 py-2 bg-slate-800 hover:bg-slate-700 border border-slate-700 rounded-lg text-sm text-slate-200 transition-colors w-full"
    >
      <i class="fas fa-folder text-claude-400" />
      <span class="truncate flex-1 text-left">{{ selectedProjectName }}</span>
      <i
        class="fas fa-chevron-down text-slate-500 transition-transform"
        :class="{ 'rotate-180': isOpen }"
      />
    </button>

    <!-- Dropdown -->
    <div
      v-if="isOpen"
      class="absolute z-50 w-full mt-2 bg-slate-800 border border-slate-700 rounded-lg shadow-xl overflow-hidden"
    >
      <!-- Search Input -->
      <div class="p-2 border-b border-slate-700">
        <div class="relative">
          <i class="fas fa-search absolute left-3 top-1/2 -translate-y-1/2 text-slate-500 text-sm" />
          <input
            v-model="searchQuery"
            type="text"
            placeholder="Search projects..."
            class="w-full pl-9 pr-3 py-2 bg-slate-900 border border-slate-600 rounded text-sm text-slate-200 placeholder-slate-500 focus:outline-none focus:border-claude-500"
          />
        </div>
      </div>

      <!-- Project List -->
      <div class="max-h-64 overflow-y-auto">
        <!-- All Projects Option -->
        <button
          @click="selectProject(null)"
          class="w-full px-4 py-2 text-left text-sm hover:bg-slate-700 transition-colors flex items-center gap-2"
          :class="{ 'bg-slate-700 text-claude-400': !currentProject, 'text-slate-300': currentProject }"
        >
          <i class="fas fa-globe text-slate-500" />
          <span>All Projects</span>
          <i v-if="!currentProject" class="fas fa-check ml-auto text-claude-400" />
        </button>

        <!-- Loading State -->
        <div v-if="loading" class="px-4 py-3 text-slate-500 text-sm text-center">
          <i class="fas fa-spinner fa-spin mr-2" />
          Loading projects...
        </div>

        <!-- No Results -->
        <div v-else-if="filteredProjects.length === 0" class="px-4 py-3 text-slate-500 text-sm text-center">
          No projects found
        </div>

        <!-- Project Items -->
        <button
          v-else
          v-for="project in filteredProjects"
          :key="project.id"
          :title="project.id"
          @click="selectProject(project.id)"
          class="w-full px-4 py-2 text-left text-sm hover:bg-slate-700 transition-colors flex items-center gap-2"
          :class="{ 'bg-slate-700 text-claude-400': currentProject === project.id, 'text-slate-300': currentProject !== project.id }"
        >
          <i class="fas fa-folder text-slate-500" />
          <span class="min-w-0 flex-1">
            <span class="block truncate">{{ project.text }}</span>
            <!-- The hash is only shown when it is needed: when several projects share a name. -->
            <span v-if="project.namesake" class="block truncate text-xs text-slate-500 font-mono">{{ project.id }}</span>
          </span>
          <span v-if="project.note" class="text-xs text-slate-500 whitespace-nowrap">{{ project.note }}</span>
          <i v-if="currentProject === project.id" class="fas fa-check text-claude-400" />
        </button>
      </div>

      <!-- Footer -->
      <div class="border-t border-slate-700">
        <button
          class="w-full px-4 py-2 text-left text-sm text-slate-400 hover:bg-slate-700 hover:text-slate-200 transition-colors flex items-center gap-2"
          @click="openManager"
        >
          <i class="fas fa-sliders" />
          <span>Manage projects…</span>
        </button>
      </div>
    </div>

    <ProjectManager v-if="showManager" @close="showManager = false" @changed="onProjectsChanged" />
  </div>
</template>
