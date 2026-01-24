<script setup lang="ts">
import { ref, computed } from 'vue'

const props = withDefaults(defineProps<{
  type?: 'info' | 'warning' | 'danger' | 'success' | 'tip'
  title?: string
  closable?: boolean
}>(), {
  type: 'info',
  closable: false
})

const isVisible = ref(true)

const close = () => {
  isVisible.value = false
}

const iconPath = computed(() => {
  switch (props.type) {
    case 'info':
      return 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z'
    case 'warning':
      return 'M1 21h22L12 2 1 21zm12-3h-2v-2h2v2zm0-4h-2v-4h2v4z'
    case 'danger':
      return 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z'
    case 'success':
      return 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm-2 15l-5-5 1.41-1.41L10 14.17l7.59-7.59L19 8l-9 9z'
    case 'tip':
      return 'M9 21c0 .55.45 1 1 1h4c.55 0 1-.45 1-1v-1H9v1zm3-19C8.14 2 5 5.14 5 9c0 2.38 1.19 4.47 3 5.74V17c0 .55.45 1 1 1h6c.55 0 1-.45 1-1v-2.26c1.81-1.27 3-3.36 3-5.74 0-3.86-3.14-7-7-7z'
    default:
      return 'M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z'
  }
})
</script>

<template>
  <div v-if="isVisible" class="alert" :class="[`alert--${type}`]">
    <div class="alert__icon">
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="20" height="20">
        <path :d="iconPath" fill="currentColor" />
      </svg>
    </div>
    <div class="alert__content">
      <div v-if="title" class="alert__title">{{ title }}</div>
      <div class="alert__message"><slot /></div>
    </div>
    <button v-if="closable" class="alert__close" @click="close" aria-label="Close alert">
      <svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" width="18" height="18">
        <path d="M19 6.41L17.59 5 12 10.59 6.41 5 5 6.41 10.59 12 5 17.59 6.41 19 12 13.41 17.59 19 19 17.59 13.41 12z" fill="currentColor" />
      </svg>
    </button>
  </div>
</template>

<style scoped>
.alert {
  display: flex;
  align-items: flex-start;
  gap: 12px;
  padding: 16px 20px;
  margin: 16px 0;
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-left: 4px solid rgba(255, 255, 255, 0.3);
  border-radius: 16px;
  color: #F5F5F7;
  transition: all 0.2s ease;
}

.alert:hover {
  background: rgba(66, 66, 69, 0.72);
}

.alert--info { border-left-color: rgba(255, 255, 255, 0.3); }
.alert--tip { border-left-color: rgba(255, 255, 255, 0.4); }
.alert--success { border-left-color: rgba(255, 255, 255, 0.5); }
.alert--warning { border-left-color: rgba(255, 255, 255, 0.6); }
.alert--danger { border-left-color: rgba(255, 255, 255, 0.8); }

.alert__icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 24px;
  height: 24px;
  color: rgba(255, 255, 255, 0.8);
}

.alert--danger .alert__icon { color: #F5F5F7; }
.alert--warning .alert__icon { color: rgba(255, 255, 255, 0.9); }

.alert__content {
  flex: 1;
  min-width: 0;
}

.alert__title {
  font-weight: 600;
  font-size: 14px;
  color: #F5F5F7;
  margin-bottom: 4px;
}

.alert__message {
  font-size: 14px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.8);
}

.alert__message :deep(p) { margin: 0; }
.alert__message :deep(p + p) { margin-top: 8px; }
.alert__message :deep(code) {
  background: rgba(0, 0, 0, 0.3);
  padding: 2px 6px;
  border-radius: 4px;
  font-size: 13px;
}

.alert__message :deep(a) {
  color: #F5F5F7;
  text-decoration: underline;
  text-decoration-color: rgba(134, 134, 139, 0.5);
}

.alert__message :deep(a:hover) {
  text-decoration-color: #F5F5F7;
}

.alert__close {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  background: transparent;
  border: none;
  border-radius: 50%;
  color: rgba(134, 134, 139, 1.0);
  cursor: pointer;
  transition: all 0.2s ease;
}

.alert__close:hover {
  background: rgba(134, 134, 139, 0.2);
  color: #F5F5F7;
}
</style>
