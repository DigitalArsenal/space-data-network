<script setup lang="ts">
import { ref, onMounted, computed } from 'vue'

interface Props {
  type?: 'success' | 'error' | 'warning' | 'info'
  message: string
  duration?: number
  closable?: boolean
}

const props = withDefaults(defineProps<Props>(), {
  type: 'info',
  duration: 5000,
  closable: true
})

const emit = defineEmits<{
  close: []
}>()

const progress = ref(100)
const isPaused = ref(false)
let intervalId: number | null = null

const icon = computed(() => {
  switch (props.type) {
    case 'success':
      return `<path d="M20 6L9 17L4 12" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`
    case 'error':
      return `<path d="M18 6L6 18M6 6L18 18" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`
    case 'warning':
      return `<path d="M12 9V13M12 17H12.01M5.07 19H18.93C20.14 19 20.93 17.67 20.32 16.61L13.39 4.21C12.78 3.15 11.22 3.15 10.61 4.21L3.68 16.61C3.07 17.67 3.86 19 5.07 19Z" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`
    default:
      return `<path d="M12 16V12M12 8H12.01M22 12C22 17.52 17.52 22 12 22C6.48 22 2 17.52 2 12C2 6.48 6.48 2 12 2C17.52 2 22 6.48 22 12Z" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>`
  }
})

function startTimer() {
  if (props.duration <= 0) return

  const step = 100 / (props.duration / 50)
  intervalId = window.setInterval(() => {
    if (!isPaused.value) {
      progress.value -= step
      if (progress.value <= 0) {
        close()
      }
    }
  }, 50)
}

function close() {
  if (intervalId) {
    clearInterval(intervalId)
  }
  emit('close')
}

function pause() {
  isPaused.value = true
}

function resume() {
  isPaused.value = false
}

onMounted(() => {
  startTimer()
})
</script>

<template>
  <div
    class="toast"
    :class="[`toast-${type}`]"
    @mouseenter="pause"
    @mouseleave="resume"
  >
    <div class="toast-icon">
      <svg width="20" height="20" viewBox="0 0 24 24" fill="none" v-html="icon" />
    </div>
    <div class="toast-content">
      <p class="toast-message">{{ message }}</p>
    </div>
    <button
      v-if="closable"
      class="toast-close"
      @click="close"
      aria-label="Close"
    >
      <svg width="16" height="16" viewBox="0 0 24 24" fill="none">
        <path
          d="M18 6L6 18M6 6L18 18"
          stroke="currentColor"
          stroke-width="2"
          stroke-linecap="round"
          stroke-linejoin="round"
        />
      </svg>
    </button>
    <div
      v-if="duration > 0"
      class="toast-progress"
      :style="{ width: `${progress}%` }"
    />
  </div>
</template>

<style scoped>
.toast {
  position: relative;
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 16px;
  background: rgba(42, 42, 45, 0.95);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 16px;
  min-width: 300px;
  max-width: 400px;
  overflow: hidden;
}

.toast-icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  color: rgba(255, 255, 255, 0.8);
}

.toast-success .toast-icon {
  color: #4ade80;
}

.toast-error .toast-icon {
  color: #f87171;
}

.toast-warning .toast-icon {
  color: #fbbf24;
}

.toast-info .toast-icon {
  color: #60a5fa;
}

.toast-content {
  flex: 1;
  min-width: 0;
}

.toast-message {
  margin: 0;
  font-size: 0.95rem;
  color: #F5F5F7;
  line-height: 1.5;
}

.toast-close {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  background: transparent;
  border: none;
  color: rgba(134, 134, 139, 1);
  cursor: pointer;
  transition: color 0.2s ease;
}

.toast-close:hover {
  color: #F5F5F7;
}

.toast-progress {
  position: absolute;
  bottom: 0;
  left: 0;
  height: 3px;
  background: rgba(255, 255, 255, 0.3);
  transition: width 0.05s linear;
}

.toast-success .toast-progress {
  background: #4ade80;
}

.toast-error .toast-progress {
  background: #f87171;
}

.toast-warning .toast-progress {
  background: #fbbf24;
}

.toast-info .toast-progress {
  background: #60a5fa;
}
</style>
