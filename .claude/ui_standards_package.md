# UI Standards Package Tasks

## Task 1: Complete Dark Theme Coverage

The current dark theme (webui/src/sdn-theme.css) only overrides a handful of Tachyons utility classes. Buttons, form elements, dropdowns, modals, tooltips, and many component backgrounds are still light-colored.

### Requirements
- Override ALL remaining light-colored Tachyons classes used in the webui:
  - Buttons: `.bg-aqua`, `.bg-teal`, `.btn-primary`, `.bg-green`, `.bg-red`, `.bg-yellow`, etc.
  - Buttons must use `var(--sdn-bg-tertiary)` backgrounds with `var(--sdn-text-primary)` text, accent-colored borders
  - Hover states must be visible against dark backgrounds
  - `.ba`, `.b--light-gray`, `.b--silver` borders must map to `var(--sdn-border)`
- Override form elements: `select`, `option`, `textarea`, `input[type=text]`, `input[type=search]`
- Override specific IPFS webui components that have inline or class-based light styles:
  - Box component backgrounds (`.bg-white` inside `Box.js`)
  - Modal/dialog overlays
  - Dropdown menus
  - Toast notifications
  - Table rows and alternating colors
  - Breadcrumbs and navigation links
  - File explorer backgrounds
  - Settings page panels
  - Status page boxes
- Make sure the header bar at top (`sdn-header-bar`) uses `var(--sdn-bg-header)` and NOT the inline `#F0F6FA` - remove the inline style from App.js
- Ensure `main.bg-white` maps to `var(--sdn-bg-primary)` (already done but verify)
- Add dark overrides for ReactVirtualized table components used in PeersTable and FilesTable
- Add dark overrides for the Joyride tour component
- Override `.white-70`, `.dark-gray`, `.mid-gray`, `.near-black`, `.light-gray` text/bg classes

### Files to modify
- `webui/src/sdn-theme.css` - Add comprehensive dark overrides
- `webui/src/App.js` - Remove inline `background: '#F0F6FA'` from header, use CSS class only

---

## Task 2: SDN Logo Upgrade

Replace the cheesy text-only "SDN" in the navbar with the orbital SVG logo from the docs site (docs/index.html favicon).

### Requirements
- Use this SVG (from docs/index.html line 12) as the logo, adapted for the navbar:
  ```svg
  <svg viewBox='0 0 100 100'><circle cx='50' cy='50' r='45' fill='none' stroke='currentColor' stroke-width='4'/><ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' stroke-width='2'/><ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' stroke-width='2' transform='rotate(60 50 50)'/><ellipse cx='50' cy='50' rx='45' ry='18' fill='none' stroke='currentColor' stroke-width='2' transform='rotate(120 50 50)'/><circle cx='50' cy='50' r='8' fill='currentColor'/></svg>
  ```
- Create as a React component: `webui/src/icons/SdnLogo.js`
- Logo color should be `#58a6ff` (the accent blue) with a subtle glow effect on hover
- In the vertical navbar (desktop/large screens): show logo at ~40px with "SDN" text below it at 14px
- In the horizontal navbar (mobile): show logo at ~28px with "SDN" text next to it
- Remove the old inline-styled text-only branding from NavBar.js

### Files to create
- `webui/src/icons/SdnLogo.js`

### Files to modify
- `webui/src/navigation/NavBar.js`

---

## Task 3: Icon Enhancement

Make navigation icons pop more in the dark theme.

### Requirements
- The existing stroke icons (StrokeMarketing, StrokeWeb, StrokeCube, etc.) are SVGs with `.fill-current-color` class
- In dark mode, active icons should glow with the accent blue color (`#58a6ff`)
- Inactive icons should be at 70% opacity (currently 50%, too dim)
- Add a subtle transition animation on hover (0.2s ease)
- Add CSS for the navbar icons in sdn-theme.css:
  - Active: color `#58a6ff`, filter `drop-shadow(0 0 6px rgba(88, 166, 255, 0.4))`
  - Inactive: opacity 0.7, color white
  - Hover: opacity 1.0, color `#58a6ff`
- The nav item labels should also be brighter in dark mode (currently hard to read)

### Files to modify
- `webui/src/sdn-theme.css` - Add icon enhancement styles
- `webui/src/navigation/NavBar.js` - Update opacity values from 0.5 to 0.7 for inactive

---

## Task 4: IPFS Section Minimization with Context Switch

IPFS sections throughout the UI need to be much smaller, with a toggle to bring them to the foreground.

### Requirements
- Create a `ContextSwitcher` component that toggles between "SDN" and "IPFS" views
- Place it prominently on the status page and peers page
- When "SDN" context is active (default):
  - SDN panels are full-size and prominent
  - IPFS sections are collapsed to a single summary line with expand button
  - IPFS stats show as a compact bar: "IPFS: 245 peers | 2.3 GB repo"
- When "IPFS" context is active:
  - IPFS panels are full-size (original layout)
  - SDN panels are collapsed to summary
- Store preference in localStorage
- The context switcher should be a pill-shaped toggle: `[SDN | IPFS]` with the active side highlighted in accent blue
- On the status page: replace the current two-panel layout with the context-switched layout
- On the peers page: replace the current two-panel layout with context-switched layout

### Files to create
- `webui/src/components/context-switcher/ContextSwitcher.js`
- `webui/src/bundles/sdn-context.js` - Redux bundle for context state

### Files to modify
- `webui/src/status/SdnDashboard.js` - Use ContextSwitcher, collapsible IPFS
- `webui/src/peers/SdnPeersPanel.js` - Use ContextSwitcher, collapsible IPFS
- `webui/src/peers/PeersPage.js` - Integrate with context
- `webui/src/status/StatusPage.js` - Integrate with context
- `webui/src/bundles/index.js` - Register context bundle

---

## Task 5: "Connected to SDN" Status Banner

Show connection status at the top of the UI.

### Requirements
- When connected to IPFS (which means SDN rides on it), show a status bar at the very top:
  - Green dot + "Connected to Space Data Network" when IPFS is connected
  - Red dot + "Disconnected" when IPFS is not connected
  - The bar should be slim (28px height), full width, dark background (`var(--sdn-bg-secondary)`)
  - Left-aligned: status dot + text
  - Right-aligned: peer count ("12 SDN peers, 245 IPFS peers")
- This goes above the header bar in App.js
- Use the existing `selectIpfsConnected` selector for connection state
- Use `selectSdnPeersCount` and `selectPeersCount` for peer counts

### Files to create
- `webui/src/components/sdn-status-bar/SdnStatusBar.js`

### Files to modify
- `webui/src/App.js` - Add SdnStatusBar above the header

---

## Task 6: Schema Explorer Package

Extract the schema data and explorer from spacedatastandards-site/ into a shared package.

### Requirements
- Create `spacedatastandards-site/packages/sds-schema-explorer/` as an npm package
- Move `schemas.js` and `app.js` logic into the package:
  - `packages/sds-schema-explorer/package.json` - name: `@spacedatastandards/schema-explorer`
  - `packages/sds-schema-explorer/src/schemas.js` - The SCHEMAS and SCHEMA_CATEGORIES data (from current schemas.js)
  - `packages/sds-schema-explorer/src/generators.js` - Code generators (generateFbs, generateTS, generateGo, generatePython, generateRust, generateJsonSchema)
  - `packages/sds-schema-explorer/src/api.js` - SchemaRegistryAPI object
  - `packages/sds-schema-explorer/src/index.js` - Main exports
- The spacedatastandards-site should import from this package (update its scripts to reference `./packages/sds-schema-explorer/src/`)
- Add the package to the root .gitignore exemption (it should NOT be ignored, unlike emsdk)

### Files to create
- `spacedatastandards-site/packages/sds-schema-explorer/package.json`
- `spacedatastandards-site/packages/sds-schema-explorer/src/schemas.js`
- `spacedatastandards-site/packages/sds-schema-explorer/src/generators.js`
- `spacedatastandards-site/packages/sds-schema-explorer/src/api.js`
- `spacedatastandards-site/packages/sds-schema-explorer/src/index.js`

### Files to modify
- `spacedatastandards-site/index.html` - Update script imports to use package paths
- `spacedatastandards-site/app.js` - Import from package instead of inline

---

## Task 7: Schema Explorer in WebUI

Integrate the schema explorer into the desktop/web UI as a new nav item.

### Requirements
- Add a "Schemas" nav link in NavBar.js (use StrokeIpld or create a new icon)
- Create a new route `/schemas` that renders the schema explorer
- The schema explorer should be a React component that provides:
  - Grid/list view of all ~40 schemas with search and category filtering
  - Schema detail modal with format tabs (JSON Schema, FlatBuffers, TypeScript, Go, Python, Rust)
  - Interactive field explorer with expand/collapse, x-flatbuffer annotations
  - Download buttons for individual schemas
- Import schema data from `@spacedatastandards/schema-explorer` package (copy the data file into webui for now since it can't npm-link easily)
- Style using the existing sdn-theme.css variables so it works in both dark and light mode
- The schema card design should match the spacedatastandards-site aesthetic (dark cards with category badges)

### Files to create
- `webui/src/schemas/SchemasPage.js` - Main schemas page component
- `webui/src/schemas/SchemaCard.js` - Schema card component
- `webui/src/schemas/SchemaModal.js` - Schema detail modal with tabs
- `webui/src/schemas/FieldExplorer.js` - Interactive field tree
- `webui/src/schemas/CodeView.js` - Code generation view
- `webui/src/schemas/schema-data.js` - Copy of SCHEMAS/SCHEMA_CATEGORIES/generators from the package
- `webui/src/schemas/SchemasPage.css` - Styles for schema components

### Files to modify
- `webui/src/navigation/NavBar.js` - Add Schemas nav link
- `webui/src/bundles/routes.js` (or wherever routes are defined) - Add /schemas route
