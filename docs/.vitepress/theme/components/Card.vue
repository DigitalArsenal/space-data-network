<script setup lang="ts">
withDefaults(defineProps<{
  title?: string
  icon?: string
  href?: string
  external?: boolean
}>(), {
  external: false
})
</script>

<template>
  <component
    :is="href ? 'a' : 'div'"
    class="card"
    :class="{ 'card--link': href }"
    :href="href"
    :target="external ? '_blank' : undefined"
    :rel="external ? 'noopener noreferrer' : undefined"
  >
    <div v-if="icon" class="card__icon">
      <slot name="icon">
        <span v-html="icon" />
      </slot>
    </div>
    <div class="card__content">
      <h3 v-if="title" class="card__title">
        {{ title }}
        <svg v-if="href && external" class="card__external" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
          <path d="M18 13v6a2 2 0 01-2 2H5a2 2 0 01-2-2V8a2 2 0 012-2h6M15 3h6v6M10 14L21 3" />
        </svg>
      </h3>
      <div class="card__body">
        <slot />
      </div>
    </div>
  </component>
</template>

<style scoped>
.card {
  display: flex;
  gap: 16px;
  padding: 20px;
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 20px;
  color: inherit;
  text-decoration: none;
  transition: all 0.2s ease;
}

.card--link:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
  transform: translateY(-2px);
}

.card__icon {
  flex-shrink: 0;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  background: rgba(0, 0, 0, 0.3);
  border-radius: 12px;
  color: #F5F5F7;
}

.card__content {
  flex: 1;
  min-width: 0;
}

.card__title {
  display: flex;
  align-items: center;
  gap: 6px;
  margin: 0 0 8px 0;
  font-size: 16px;
  font-weight: 600;
  color: #F5F5F7;
}

.card__external {
  opacity: 0.5;
}

.card__body {
  font-size: 14px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.7);
}

.card__body :deep(p) {
  margin: 0;
}
</style>
