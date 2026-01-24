<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  variant?: 'primary' | 'secondary' | 'ghost'
  size?: 'sm' | 'md' | 'lg'
  disabled?: boolean
  loading?: boolean
  href?: string
  external?: boolean
}>(), {
  variant: 'primary',
  size: 'md',
  disabled: false,
  loading: false,
  external: false
})

const emit = defineEmits<{
  click: [event: MouseEvent]
}>()

const tag = computed(() => props.href ? 'a' : 'button')

const attrs = computed(() => {
  if (props.href) {
    return {
      href: props.href,
      target: props.external ? '_blank' : undefined,
      rel: props.external ? 'noopener noreferrer' : undefined
    }
  }
  return {
    type: 'button',
    disabled: props.disabled || props.loading
  }
})

function handleClick(e: MouseEvent) {
  if (!props.disabled && !props.loading) {
    emit('click', e)
  }
}
</script>

<template>
  <component
    :is="tag"
    class="btn"
    :class="[
      `btn--${variant}`,
      `btn--${size}`,
      { 'btn--disabled': disabled, 'btn--loading': loading }
    ]"
    v-bind="attrs"
    @click="handleClick"
  >
    <span v-if="loading" class="btn__spinner">
      <svg viewBox="0 0 24 24" fill="none">
        <circle cx="12" cy="12" r="10" stroke="currentColor" stroke-width="3" stroke-linecap="round" opacity="0.25" />
        <path d="M12 2a10 10 0 0 1 10 10" stroke="currentColor" stroke-width="3" stroke-linecap="round" />
      </svg>
    </span>
    <span class="btn__content" :class="{ 'btn__content--hidden': loading }">
      <slot />
    </span>
  </component>
</template>

<style scoped>
.btn {
  position: relative;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 8px;
  font-family: inherit;
  font-weight: 500;
  text-decoration: none;
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 28px;
  cursor: pointer;
  transition: all 0.2s ease;
  white-space: nowrap;
}

/* Sizes */
.btn--sm {
  padding: 8px 16px;
  font-size: 13px;
}

.btn--md {
  padding: 12px 24px;
  font-size: 14px;
}

.btn--lg {
  padding: 16px 32px;
  font-size: 16px;
}

/* Primary */
.btn--primary {
  background: #F5F5F7;
  color: #000000;
  border-color: transparent;
}

.btn--primary:hover:not(.btn--disabled) {
  background: #FFFFFF;
  transform: translateY(-1px);
}

.btn--primary:active:not(.btn--disabled) {
  transform: translateY(0);
}

/* Secondary */
.btn--secondary {
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  color: #F5F5F7;
}

.btn--secondary:hover:not(.btn--disabled) {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

/* Ghost */
.btn--ghost {
  background: transparent;
  color: #F5F5F7;
  border-color: transparent;
}

.btn--ghost:hover:not(.btn--disabled) {
  background: rgba(255, 255, 255, 0.1);
}

/* States */
.btn--disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.btn--loading {
  cursor: wait;
}

.btn__spinner {
  position: absolute;
  display: flex;
  align-items: center;
  justify-content: center;
}

.btn__spinner svg {
  width: 20px;
  height: 20px;
  animation: spin 1s linear infinite;
}

@keyframes spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.btn__content--hidden {
  visibility: hidden;
}
</style>
