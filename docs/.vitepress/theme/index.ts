import DefaultTheme from 'vitepress/theme'
import type { Theme } from 'vitepress'
import './custom.css'

// Layout components
import VideoBackground from './components/VideoBackground.vue'
import VideoCard from './components/VideoCard.vue'
import Accordion from './components/Accordion.vue'
import AccordionItem from './components/AccordionItem.vue'
import BaseModal from './components/BaseModal.vue'
import Toast from './components/Toast.vue'
import HomeHero from './components/HomeHero.vue'
import HomeLayout from './components/HomeLayout.vue'

// UI components
import Alert from './components/Alert.vue'
import Button from './components/Button.vue'
import Badge from './components/Badge.vue'
import Card from './components/Card.vue'
import Icon from './components/Icon.vue'
import CopyButton from './components/CopyButton.vue'
import Tooltip from './components/Tooltip.vue'
import Progress from './components/Progress.vue'
import Tabs from './components/Tabs.vue'
import Tab from './components/Tab.vue'

// Grid components
import StatsGrid from './components/StatsGrid.vue'
import FeatureGrid from './components/FeatureGrid.vue'

export default {
  extends: DefaultTheme,
  enhanceApp({ app }) {
    // Layout components
    app.component('VideoBackground', VideoBackground)
    app.component('VideoCard', VideoCard)
    app.component('Accordion', Accordion)
    app.component('AccordionItem', AccordionItem)
    app.component('BaseModal', BaseModal)
    app.component('Toast', Toast)
    app.component('HomeHero', HomeHero)
    app.component('HomeLayout', HomeLayout)

    // UI components
    app.component('Alert', Alert)
    app.component('Button', Button)
    app.component('Badge', Badge)
    app.component('Card', Card)
    app.component('Icon', Icon)
    app.component('CopyButton', CopyButton)
    app.component('Tooltip', Tooltip)
    app.component('Progress', Progress)
    app.component('Tabs', Tabs)
    app.component('Tab', Tab)

    // Grid components
    app.component('StatsGrid', StatsGrid)
    app.component('FeatureGrid', FeatureGrid)
  }
} satisfies Theme
