<script lang="ts">
import type { InjectionKey, Ref } from 'vue'

export interface TabsContext {
  activeTab: Ref<string>
  registerTab: (id: string, title: string) => void
  selectTab: (id: string) => void
  tabs: Ref<{ id: string; title: string }[]>
}

export const TabsKey: InjectionKey<TabsContext> = Symbol('Tabs')
</script>

<script setup lang="ts">
import { provide, ref, onMounted } from 'vue'

const props = withDefaults(defineProps<{
  defaultTab?: string
}>(), {})

const emit = defineEmits<{
  change: [tabId: string]
}>()

const tabs = ref<{ id: string; title: string }[]>([])
const activeTab = ref<string>('')

const registerTab = (id: string, title: string) => {
  const exists = tabs.value.find(t => t.id === id)
  if (!exists) {
    tabs.value.push({ id, title })
    if (!activeTab.value) {
      activeTab.value = id
    }
  }
}

const selectTab = (id: string) => {
  activeTab.value = id
  emit('change', id)
}

onMounted(() => {
  if (props.defaultTab) {
    activeTab.value = props.defaultTab
  }
})

provide(TabsKey, {
  activeTab,
  registerTab,
  selectTab,
  tabs
})
</script>

<template>
  <div class="tabs">
    <div class="tabs__list" role="tablist">
      <button
        v-for="tab in tabs"
        :key="tab.id"
        :id="`tab-${tab.id}`"
        role="tab"
        :aria-selected="activeTab === tab.id"
        :aria-controls="`tabpanel-${tab.id}`"
        class="tabs__button"
        :class="{ 'tabs__button--active': activeTab === tab.id }"
        @click="selectTab(tab.id)"
      >
        {{ tab.title }}
      </button>
    </div>
    <div class="tabs__panels">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.tabs {
  width: 100%;
  margin: 16px 0;
}

.tabs__list {
  display: flex;
  gap: 4px;
  padding: 4px;
  background: rgba(42, 42, 45, 0.5);
  border-radius: 16px;
  overflow-x: auto;
}

.tabs__button {
  padding: 10px 20px;
  background: transparent;
  border: none;
  border-radius: 12px;
  color: rgba(255, 255, 255, 0.6);
  font-size: 14px;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
  transition: all 0.2s ease;
}

.tabs__button:hover {
  color: rgba(255, 255, 255, 0.9);
  background: rgba(255, 255, 255, 0.05);
}

.tabs__button--active {
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  color: #F5F5F7;
}

.tabs__panels {
  margin-top: 16px;
}
</style>
