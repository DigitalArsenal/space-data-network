<script lang="ts">
  import type { StatusLevel } from '../../lib/tokens';

  let {
    level = 'nominal',
    pulse = false,
    label = undefined,
  }: {
    level?: StatusLevel;
    pulse?: boolean;
    label?: string;
  } = $props();
</script>

<span
  class={`sa-dot sa-dot--${level}${pulse ? ' sa-dot--pulse' : ''}`}
  role="status"
  title={label ?? level.toUpperCase()}
  aria-label={label ?? level.toUpperCase()}
></span>
