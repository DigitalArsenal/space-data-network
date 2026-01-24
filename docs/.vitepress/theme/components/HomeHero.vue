<script setup lang="ts">
import { ref, onMounted, onUnmounted } from 'vue'

interface Props {
  videoSrc?: string
}

const props = withDefaults(defineProps<Props>(), {
  videoSrc: 'https://customer-cbeadsgr09pnsezs.cloudflarestream.com/90bb1b34646b81b3b63e5a854ea00da3/manifest/video.m3u8'
})

const videoRef = ref<HTMLVideoElement | null>(null)
const isLoaded = ref(false)
let hls: any = null

async function initHls() {
  if (!videoRef.value || !props.videoSrc) return

  // Check if the browser supports HLS natively (Safari)
  if (videoRef.value.canPlayType('application/vnd.apple.mpegurl')) {
    videoRef.value.src = props.videoSrc
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

      hls.loadSource(props.videoSrc)
      hls.attachMedia(videoRef.value)

      hls.on(Hls.Events.MANIFEST_PARSED, () => {
        isLoaded.value = true
        videoRef.value?.play().catch(() => {})
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

onMounted(() => {
  initHls()
})

onUnmounted(() => {
  destroyHls()
})
</script>

<template>
  <div class="home-hero">
    <!-- Video Background -->
    <div class="hero-video-container">
      <video
        ref="videoRef"
        class="hero-video"
        :class="{ 'is-loaded': isLoaded }"
        autoplay
        loop
        muted
        playsinline
        disablePictureInPicture
      />
      <div class="hero-video-overlay" />
    </div>

    <!-- Hero Content -->
    <div class="hero-content">
      <div class="hero-inner">
        <h1 class="hero-title">
          <span class="hero-title-line">Space Data</span>
          <span class="hero-title-line">Network</span>
        </h1>
        <p class="hero-tagline">
          Open infrastructure for global collaboration on space situational awareness
        </p>
        <p class="hero-subtitle">
          Decentralized peer-to-peer data exchange built on IPFS
        </p>
        <div class="hero-actions">
          <a href="/guide/getting-started" class="hero-button primary">
            Get Started
          </a>
          <a href="https://github.com/DigitalArsenal/go-space-data-network" class="hero-button secondary" target="_blank">
            View on GitHub
          </a>
          <a href="/downloads" class="hero-button secondary">
            Downloads
          </a>
        </div>
      </div>
    </div>

    <!-- Scroll Indicator -->
    <div class="scroll-indicator">
      <div class="scroll-arrow" />
    </div>
  </div>
</template>

<style scoped>
.home-hero {
  position: relative;
  min-height: 100vh;
  display: flex;
  align-items: center;
  justify-content: center;
  overflow: hidden;
  background: #000000;
}

.hero-video-container {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  z-index: 0;
}

.hero-video {
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
  transition: opacity 1s ease;
}

.hero-video.is-loaded {
  opacity: 1;
}

.hero-video-overlay {
  position: absolute;
  top: 0;
  left: 0;
  width: 100%;
  height: 100%;
  background: rgba(0, 0, 0, 0.5);
  z-index: 1;
}

.hero-content {
  position: relative;
  z-index: 2;
  width: 100%;
  max-width: 1440px;
  margin: 0 auto;
  padding: 0 64px;
  text-align: center;
}

.hero-inner {
  max-width: 900px;
  margin: 0 auto;
}

.hero-title {
  font-size: 80px;
  font-weight: 600;
  line-height: 1.05;
  color: #F5F5F7;
  margin: 0;
  letter-spacing: -0.02em;
}

.hero-title-line {
  display: block;
}

.hero-tagline {
  font-size: 28px;
  font-weight: 600;
  line-height: 1.15;
  color: rgba(255, 255, 255, 0.8);
  margin: 24px 0 0;
}

.hero-subtitle {
  font-size: 22px;
  font-weight: 400;
  line-height: 1.4;
  color: rgba(134, 134, 139, 1.0);
  margin: 16px 0 0;
}

.hero-actions {
  display: flex;
  gap: 16px;
  justify-content: center;
  margin-top: 48px;
  flex-wrap: wrap;
}

.hero-button {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  padding: 16px 32px;
  font-size: 18px;
  font-weight: 600;
  text-decoration: none;
  border-radius: 28px;
  backdrop-filter: blur(20px);
  -webkit-backdrop-filter: blur(20px);
  transition: all 0.2s ease;
}

.hero-button.primary {
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  color: #F5F5F7;
}

.hero-button.primary:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

.hero-button.secondary {
  background: rgba(42, 42, 45, 0.72);
  border: 1px solid rgba(134, 134, 139, 0.3);
  color: #F5F5F7;
}

.hero-button.secondary:hover {
  background: rgba(66, 66, 69, 0.72);
  border-color: rgba(134, 134, 139, 0.5);
}

.scroll-indicator {
  position: absolute;
  bottom: 48px;
  left: 50%;
  transform: translateX(-50%);
  z-index: 2;
}

.scroll-arrow {
  width: 24px;
  height: 24px;
  border-right: 2px solid rgba(134, 134, 139, 0.5);
  border-bottom: 2px solid rgba(134, 134, 139, 0.5);
  transform: rotate(45deg);
  animation: scroll-bounce 2s infinite;
}

@keyframes scroll-bounce {
  0%, 20%, 50%, 80%, 100% {
    transform: rotate(45deg) translateY(0);
  }
  40% {
    transform: rotate(45deg) translateY(8px);
  }
  60% {
    transform: rotate(45deg) translateY(4px);
  }
}

/* Responsive */
@media (max-width: 1068px) {
  .hero-title {
    font-size: 64px;
  }
  .hero-tagline {
    font-size: 24px;
  }
  .hero-subtitle {
    font-size: 18px;
  }
  .hero-content {
    padding: 0 32px;
  }
}

@media (max-width: 734px) {
  .hero-title {
    font-size: 48px;
  }
  .hero-tagline {
    font-size: 20px;
  }
  .hero-subtitle {
    font-size: 16px;
  }
  .hero-content {
    padding: 0 24px;
  }
  .hero-actions {
    flex-direction: column;
    align-items: center;
  }
  .hero-button {
    width: 100%;
    max-width: 280px;
  }
}
</style>
