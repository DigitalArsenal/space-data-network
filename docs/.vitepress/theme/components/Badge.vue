<script setup lang="ts">
withDefaults(defineProps<{
  type?: 'default' | 'info' | 'success' | 'warning' | 'danger'
  size?: 'sm' | 'md'
}>(), {
  type: 'default',
  size: 'md'
})
</script>

<template>
  <span class="badge" :class="[`badge--${type}`, `badge--${size}`]">
    <slot />
  </span>
</template>

<style scoped>
.badge {
  display: inline-flex;
  align-items: center;
  font-weight: 500;
  border-radius: 100px;
  white-space: nowrap;
}

.badge--sm {
  padding: 2px 8px;
  font-size: 11px;
}

.badge--md {
  padding: 4px 12px;
  font-size: 12px;
}

.badge--default {
  background: rgba(134, 134, 139, 0.2);
  color: rgba(255, 255, 255, 0.8);
}

.badge--info {
  background: rgba(96, 165, 250, 0.2);
  color: rgba(96, 165, 250, 1);
}

.badge--success {
  background: rgba(74, 222, 128, 0.2);
  color: rgba(74, 222, 128, 1);
}

.badge--warning {
  background: rgba(251, 191, 36, 0.2);
  color: rgba(251, 191, 36, 1);
}

.badge--danger {
  background: rgba(248, 113, 113, 0.2);
  color: rgba(248, 113, 113, 1);
}
</style>
