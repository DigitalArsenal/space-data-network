<script setup lang="ts">
defineProps<{
  stats: {
    label: string
    value: string | number
    description?: string
  }[]
}>()
</script>

<template>
  <div class="stats-grid">
    <div v-for="stat in stats" :key="stat.label" class="stat-card">
      <div class="stat-card__value">{{ stat.value }}</div>
      <div class="stat-card__label">{{ stat.label }}</div>
      <div v-if="stat.description" class="stat-card__description">
        {{ stat.description }}
      </div>
    </div>
  </div>
</template>

<style scoped>
.stats-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
  gap: 16px;
  margin: 24px 0;
}

.stat-card {
  padding: 24px;
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border: 1px solid rgba(134, 134, 139, 0.3);
  border-radius: 20px;
  text-align: center;
  transition: all 0.2s ease;
}

.stat-card:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
  transform: translateY(-2px);
}

.stat-card__value {
  font-size: 2.5rem;
  font-weight: 700;
  color: #F5F5F7;
  line-height: 1.2;
}

.stat-card__label {
  margin-top: 8px;
  font-size: 14px;
  font-weight: 500;
  color: rgba(255, 255, 255, 0.8);
  text-transform: uppercase;
  letter-spacing: 0.5px;
}

.stat-card__description {
  margin-top: 8px;
  font-size: 13px;
  color: rgba(255, 255, 255, 0.6);
  line-height: 1.4;
}

@media (max-width: 640px) {
  .stats-grid {
    grid-template-columns: repeat(2, 1fr);
  }

  .stat-card {
    padding: 16px;
  }

  .stat-card__value {
    font-size: 1.75rem;
  }
}
</style>
