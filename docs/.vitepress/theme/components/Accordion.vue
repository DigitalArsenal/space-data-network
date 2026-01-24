<script setup lang="ts">
import { ref, provide } from 'vue'

interface Props {
  allowMultiple?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  allowMultiple: false
})

const openItems = ref<Set<string>>(new Set())

function toggleItem(id: string) {
  if (openItems.value.has(id)) {
    openItems.value.delete(id)
  } else {
    if (!props.allowMultiple) {
      openItems.value.clear()
    }
    openItems.value.add(id)
  }
  openItems.value = new Set(openItems.value)
}

function isOpen(id: string): boolean {
  return openItems.value.has(id)
}

provide('accordion', { toggleItem, isOpen })
</script>

<template>
  <div class="accordion">
    <slot />
  </div>
</template>

<style scoped>
.accordion {
  display: flex;
  flex-direction: column;
  gap: 8px;
}
</style>
