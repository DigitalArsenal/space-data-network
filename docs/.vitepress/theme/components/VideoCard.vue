<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

interface Props {
  src: string
  title?: string
  description?: string
  link?: string
  poster?: string
  overlayOpacity?: number
}

const props = withDefaults(defineProps<Props>(), {
  overlayOpacity: 0.7
})

const videoRef = ref<HTMLVideoElement | null>(null)
const cardRef = ref<HTMLElement | null>(null)
const isLoaded = ref(false)
const isHovered = ref(false)
let hls: any = null

async function initHls() {
  if (!videoRef.value || !props.src) return

  // Check if the browser supports HLS natively (Safari)
  if (videoRef.value.canPlayType('application/vnd.apple.mpegurl')) {
    videoRef.value.src = props.src
    videoRef.value.addEventListener('loadeddata', () => {
      isLoaded.value = true
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
          // Autoplay failed
        })
      })

      hls.on(Hls.Events.ERROR, (_event: any, data: any) => {
        if (data.fatal) {
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
    }
  } catch (e) {
    console.error('Failed to load HLS.js:', e)
  }
}

function destroyHls() {
  if (hls) {
    hls.destroy()
    hls = null
  }
}

function handleMouseEnter() {
  isHovered.value = true
  videoRef.value?.play().catch(() => {})
}

function handleMouseLeave() {
  isHovered.value = false
}

onMounted(() => {
  initHls()
})

onUnmounted(() => {
  destroyHls()
})
</script>

<template>
  <component
    :is="link ? 'a' : 'div'"
    ref="cardRef"
    class="video-card"
    :class="{ 'is-hovered': isHovered }"
    :href="link"
    @mouseenter="handleMouseEnter"
    @mouseleave="handleMouseLeave"
  >
    <div class="video-card-background">
      <video
        ref="videoRef"
        class="video-card-video"
        :class="{ 'is-loaded': isLoaded }"
        :poster="poster"
        autoplay
        loop
        muted
        playsinline
        disablePictureInPicture
      />
      <div
        class="video-card-overlay"
        :style="{ opacity: isHovered ? overlayOpacity - 0.2 : overlayOpacity }"
      />
    </div>
    <div class="video-card-content">
      <div class="video-card-glass">
        <h3 v-if="title" class="video-card-title">{{ title }}</h3>
        <p v-if="description" class="video-card-description">{{ description }}</p>
        <slot />
      </div>
    </div>
  </component>
</template>

<style scoped>
.video-card {
  position: relative;
  display: block;
  width: 100%;
  min-height: 280px;
  border-radius: 28px;
  overflow: hidden;
  background: #000000;
  border: 1px solid rgba(134, 134, 139, 0.3);
  text-decoration: none;
  transition: all 0.3s ease;
}

.video-card:hover {
  border-color: rgba(134, 134, 139, 0.5);
  transform: translateY(-4px);
}

.video-card-background {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
}

.video-card-video {
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

.video-card-video.is-loaded {
  opacity: 1;
}

.video-card-overlay {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: #000000;
  transition: opacity 0.3s ease;
}

.video-card-content {
  position: relative;
  z-index: 2;
  display: flex;
  flex-direction: column;
  justify-content: flex-end;
  width: 100%;
  height: 100%;
  min-height: 280px;
  padding: 24px;
}

.video-card-glass {
  background: rgba(42, 42, 45, 0.72);
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  border-radius: 20px;
  padding: 20px;
  border: 1px solid rgba(134, 134, 139, 0.3);
}

.video-card-title {
  margin: 0 0 8px 0;
  font-size: 1.25rem;
  font-weight: 600;
  color: #F5F5F7;
  line-height: 1.3;
}

.video-card-description {
  margin: 0;
  font-size: 0.95rem;
  color: rgba(255, 255, 255, 0.8);
  line-height: 1.5;
}

@media (max-width: 768px) {
  .video-card {
    min-height: 240px;
  }

  .video-card-content {
    min-height: 240px;
    padding: 16px;
  }

  .video-card-glass {
    padding: 16px;
  }

  .video-card-title {
    font-size: 1.1rem;
  }

  .video-card-description {
    font-size: 0.9rem;
  }
}
</style>
