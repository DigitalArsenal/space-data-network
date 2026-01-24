<script setup lang="ts">
import { ref } from 'vue'

withDefaults(defineProps<{
  text: string
  position?: 'top' | 'bottom' | 'left' | 'right'
}>(), {
  position: 'top'
})

const isVisible = ref(false)

function show() {
  isVisible.value = true
}

function hide() {
  isVisible.value = false
}
</script>

<template>
  <span
    class="tooltip-wrapper"
    @mouseenter="show"
    @mouseleave="hide"
    @focus="show"
    @blur="hide"
  >
    <slot />
    <Transition name="tooltip">
      <span
        v-if="isVisible"
        class="tooltip"
        :class="[`tooltip--${position}`]"
        role="tooltip"
      >
        {{ text }}
      </span>
    </Transition>
  </span>
</template>

<style scoped>
.tooltip-wrapper {
  position: relative;
  display: inline-flex;
}

.tooltip {
  position: absolute;
  padding: 8px 12px;
  background: rgba(42, 42, 45, 0.95);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 8px;
  color: #F5F5F7;
  font-size: 13px;
  white-space: nowrap;
  z-index: 100;
  pointer-events: none;
}

.tooltip--top {
  bottom: 100%;
  left: 50%;
  transform: translateX(-50%);
  margin-bottom: 8px;
}

.tooltip--bottom {
  top: 100%;
  left: 50%;
  transform: translateX(-50%);
  margin-top: 8px;
}

.tooltip--left {
  right: 100%;
  top: 50%;
  transform: translateY(-50%);
  margin-right: 8px;
}

.tooltip--right {
  left: 100%;
  top: 50%;
  transform: translateY(-50%);
  margin-left: 8px;
}

.tooltip-enter-active,
.tooltip-leave-active {
  transition: opacity 0.15s ease, transform 0.15s ease;
}

.tooltip-enter-from,
.tooltip-leave-to {
  opacity: 0;
}

.tooltip--top.tooltip-enter-from,
.tooltip--top.tooltip-leave-to {
  transform: translateX(-50%) translateY(4px);
}

.tooltip--bottom.tooltip-enter-from,
.tooltip--bottom.tooltip-leave-to {
  transform: translateX(-50%) translateY(-4px);
}
</style>
