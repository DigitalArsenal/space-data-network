<script setup lang="ts">
import { inject, computed, ref } from 'vue'

interface Props {
  title: string
  id?: string
}

const props = withDefaults(defineProps<Props>(), {
  id: () => `accordion-${Math.random().toString(36).substr(2, 9)}`
})

const accordion = inject<{
  toggleItem: (id: string) => void
  isOpen: (id: string) => boolean
}>('accordion')

const contentRef = ref<HTMLElement | null>(null)

const isOpen = computed(() => accordion?.isOpen(props.id) ?? false)

function toggle() {
  accordion?.toggleItem(props.id)
}

function handleKeydown(e: KeyboardEvent) {
  if (e.key === 'Enter' || e.key === ' ') {
    e.preventDefault()
    toggle()
  }
}
</script>

<template>
  <div class="accordion-item" :class="{ 'is-open': isOpen }">
    <button
      class="accordion-header"
      :aria-expanded="isOpen"
      :aria-controls="`content-${id}`"
      @click="toggle"
      @keydown="handleKeydown"
    >
      <span class="accordion-title">{{ title }}</span>
      <span class="accordion-icon">
        <svg
          width="20"
          height="20"
          viewBox="0 0 20 20"
          fill="none"
          :class="{ 'rotated': isOpen }"
        >
          <path
            d="M5 7.5L10 12.5L15 7.5"
            stroke="currentColor"
            stroke-width="2"
            stroke-linecap="round"
            stroke-linejoin="round"
          />
        </svg>
      </span>
    </button>
    <div
      :id="`content-${id}`"
      ref="contentRef"
      class="accordion-content"
      :class="{ 'is-open': isOpen }"
    >
      <div class="accordion-content-inner">
        <slot />
      </div>
    </div>
  </div>
</template>

<style scoped>
.accordion-item {
  background: #000000;
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 16px;
  overflow: hidden;
  transition: border-color 0.2s ease;
}

.accordion-item:hover {
  border-color: rgba(134, 134, 139, 0.5);
}

.accordion-item.is-open {
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
}

.accordion-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
  padding: 20px 24px;
  background: transparent;
  border: none;
  cursor: pointer;
  text-align: left;
  color: #F5F5F7;
  font-size: 1.1rem;
  font-weight: 500;
  transition: background-color 0.2s ease;
}

.accordion-header:hover {
  background: rgba(255, 255, 255, 0.05);
}

.accordion-header:focus {
  outline: none;
  box-shadow: inset 0 0 0 2px rgba(255, 255, 255, 0.3);
}

.accordion-title {
  flex: 1;
}

.accordion-icon {
  display: flex;
  align-items: center;
  justify-content: center;
  color: rgba(134, 134, 139, 1);
  transition: color 0.2s ease;
}

.accordion-icon svg {
  transition: transform 0.3s ease;
}

.accordion-icon svg.rotated {
  transform: rotate(180deg);
}

.accordion-content {
  max-height: 0;
  overflow: hidden;
  transition: max-height 0.3s ease;
}

.accordion-content.is-open {
  max-height: 1000px;
}

.accordion-content-inner {
  padding: 0 24px 20px 24px;
  color: rgba(255, 255, 255, 0.8);
  line-height: 1.6;
}

@media (max-width: 768px) {
  .accordion-header {
    padding: 16px 20px;
    font-size: 1rem;
  }

  .accordion-content-inner {
    padding: 0 20px 16px 20px;
  }
}
</style>
