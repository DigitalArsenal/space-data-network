<script setup lang="ts">
import { ref, onMounted, onUnmounted, watch } from 'vue'

interface Props {
  src: string
  poster?: string
  overlay?: boolean
  overlayOpacity?: number
}

const props = withDefaults(defineProps<Props>(), {
  overlay: true,
  overlayOpacity: 0.6
})

const videoRef = ref<HTMLVideoElement | null>(null)
const isLoaded = ref(false)
const hasError = ref(false)
let hls: any = null

async function initHls() {
  if (!videoRef.value || !props.src) return

  // Check if the browser supports HLS natively (Safari)
  if (videoRef.value.canPlayType('application/vnd.apple.mpegurl')) {
    videoRef.value.src = props.src
    videoRef.value.addEventListener('loadeddata', () => {
      isLoaded.value = true
    })
    videoRef.value.addEventListener('error', () => {
      hasError.value = true
    })
    return
  }

  // Use HLS.js for other browsers
  try {
    const Hls = (await import('hls.js')).default

    if (Hls.isSupported()) {
      hls = new Hls({
        enableWorker: true,
        lowLatencyMode: false,
        backBufferLength: 90
      })

      hls.loadSource(props.src)
      hls.attachMedia(videoRef.value)

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        isLoaded.value = true
        videoRef.value?.play().catch(() => {
          // Autoplay failed, likely due to browser policy
        })
      })

      hls.on(Hls.Events.ERROR, (_event: any, data: any) => {
        if (data.fatal) {
          hasError.value = true
          switch (data.type) {
            case Hls.ErrorTypes.NETWORK_ERROR:
              hls.startLoad()
              break
            case Hls.ErrorTypes.MEDIA_ERROR:
              hls.recoverMediaError()
              break
            default:
              destroyHls()
              break
          }
        }
      })
    } else {
      hasError.value = true
    }
  } catch (e) {
    hasError.value = true
    console.error('Failed to load HLS.js:', e)
  }
}

function destroyHls() {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

watch(() => props.src, () => {
  destroyHls()
  isLoaded.value = false
  hasError.value = false
  initHls()
})

onMounted(() => {
  initHls()
})

onUnmounted(() => {
  destroyHls()
})
</script>

<template>
  <div class="video-background">
    <video
      ref="videoRef"
      class="video-element"
      :class="{ 'is-loaded': isLoaded }"
      :poster="poster"
      autoplay
      loop
      muted
      playsinline
      disablePictureInPicture
    />
    <div
      v-if="overlay"
      class="video-overlay"
      :style="{ opacity: overlayOpacity }"
    />
    <div class="video-content">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.video-background {
  position: relative;
  width: 100%;
  height: 100%;
  overflow: hidden;
  background: #000000;
}

.video-element {
  position: absolute;
  top: 50%;
  left: 50%;
  min-width: 100%;
  min-height: 100%;
  width: auto;
  height: auto;
  transform: translate(-50%, -50%);
  object-fit: cover;
  opacity: 0;
  transition: opacity 0.5s ease;
}

.video-element.is-loaded {
  opacity: 1;
}

.video-overlay {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: #000000;
  pointer-events: none;
  z-index: 1;
}

.video-content {
  position: relative;
  z-index: 2;
  width: 100%;
  height: 100%;
}
</style>
