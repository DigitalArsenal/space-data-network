<script setup lang="ts">
import { computed } from 'vue'

const props = withDefaults(defineProps<{
  value: number
  max?: number
  showLabel?: boolean
  size?: 'sm' | 'md' | 'lg'
}>(), {
  max: 100,
  showLabel: false,
  size: 'md'
})

const percentage = computed(() => {
  return Math.min(100, Math.max(0, (props.value / props.max) * 100))
})
</script>

<template>
  <div class="progress-container" :class="[`progress--${size}`]">
    <div
      class="progress"
      role="progressbar"
      :aria-valuenow="value"
      :aria-valuemin="0"
      :aria-valuemax="max"
    >
      <div
        class="progress__bar"
        :style="{ width: `${percentage}%` }"
      />
    </div>
    <span v-if="showLabel" class="progress__label">
      {{ Math.round(percentage) }}%
    </span>
  </div>
</template>

<style scoped>
.progress-container {
  display: flex;
  align-items: center;
  gap: 12px;
}

.progress {
  flex: 1;
  background: rgba(42, 42, 45, 0.72);
  border-radius: 100px;
  overflow: hidden;
}

.progress--sm .progress {
  height: 4px;
}

.progress--md .progress {
  height: 8px;
}

.progress--lg .progress {
  height: 12px;
}

.progress__bar {
  height: 100%;
  background: #F5F5F7;
  border-radius: 100px;
  transition: width 0.3s ease;
}

.progress__label {
  flex-shrink: 0;
  font-size: 13px;
  font-weight: 500;
  color: rgba(255, 255, 255, 0.8);
  min-width: 40px;
  text-align: right;
}
</style>
