<script setup lang="ts">
defineProps<{
  features: {
    icon?: string
    title: string
    description: string
  }[]
}>()
</script>

<template>
  <div class="feature-grid">
    <div v-for="feature in features" :key="feature.title" class="feature-card">
      <div v-if="feature.icon" class="feature-card__icon" v-html="feature.icon" />
      <h3 class="feature-card__title">{{ feature.title }}</h3>
      <p class="feature-card__description">{{ feature.description }}</p>
    </div>
  </div>
</template>

<style scoped>
.feature-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(280px, 1fr));
  gap: 20px;
  margin: 24px 0;
}

.feature-card {
  padding: 24px;
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 20px;
  transition: all 0.2s ease;
}

.feature-card:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
  transform: translateY(-2px);
}

.feature-card__icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 48px;
  height: 48px;
  margin-bottom: 16px;
  background: rgba(0, 0, 0, 0.3);
  border-radius: 12px;
  color: #F5F5F7;
}

.feature-card__icon :deep(svg) {
  width: 24px;
  height: 24px;
}

.feature-card__title {
  margin: 0 0 8px 0;
  font-size: 18px;
  font-weight: 600;
  color: #F5F5F7;
}

.feature-card__description {
  margin: 0;
  font-size: 14px;
  line-height: 1.6;
  color: rgba(255, 255, 255, 0.7);
}
</style>
