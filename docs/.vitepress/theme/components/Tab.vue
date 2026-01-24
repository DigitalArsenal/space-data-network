<script setup lang="ts">
import { inject, onMounted, computed } from 'vue'
import { TabsKey } from './Tabs.vue'

const props = defineProps<{
  title: string
  id?: string
}>()

const tabsContext = inject(TabsKey)

const tabId = computed(() => props.id || props.title.toLowerCase().replace(/\s+/g, '-'))

onMounted(() => {
  if (tabsContext) {
    tabsContext.registerTab(tabId.value, props.title)
  }
})

const isActive = computed(() => tabsContext?.activeTab.value === tabId.value)
</script>

<template>
  <div
    v-show="isActive"
    :id="`tabpanel-${tabId}`"
    role="tabpanel"
    :aria-labelledby="`tab-${tabId}`"
    class="tab-panel"
  >
    <slot />
  </div>
</template>

<style scoped>
.tab-panel {
  animation: fadeIn 0.2s ease;
}

@keyframes fadeIn {
  from {
    opacity: 0;
    transform: translateY(4px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}
</style>
