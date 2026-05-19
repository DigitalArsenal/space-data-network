# SDN Logo System Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace SDN-owned logos with the approved monochrome mark, regenerate desktop/web assets, rebuild SDN Desktop, and launch it locally.

**Architecture:** Keep one SVG source for the primary SDN mark and one SVG source for the toolbar mark. Generate raster desktop/web assets from those sources so packaged surfaces stay consistent.

**Tech Stack:** SVG, ImageMagick, macOS `iconutil`, Electron desktop package, Vitest, Playwright unit tests.

---

### File Map

- Modify `.gitignore`: ignore visual-companion `.superpowers/` output.
- Modify `sdn-js/src/ui/upstream-webui/branding.test.ts`: assert the shared SDN mark SVG contract.
- Modify `desktop/test/unit/dashboard.spec.js`: assert desktop branding labels and toolbar asset contract.
- Modify `sdn-js/ui/src/upstream-webui/overrides/navigation/sdn-logo-mark.svg`: primary white SDN mark for dark nav.
- Keep upstream WebUI IPFS logo sources unchanged; SDN branding belongs in SDN-owned overlays and desktop shell assets.
- Modify `desktop/assets/build/icon.svg`: black primary SDN mark for light app-icon contexts.
- Modify `desktop/assets/pages/sdn-splash.svg`: white primary SDN mark for dark splash surfaces.
- Modify `desktop/assets/icons/tray/sdn-tray.svg`: solid toolbar triangle with cut-out dot.
- Regenerate `desktop/assets/icons/tray/macos/*Template*.png`, `desktop/assets/icons/tray/others/*.png`, `desktop/assets/build/icon.ico`, and `desktop/assets/build/icon.icns`.
- Modify `desktop/package.json`, `desktop/electron-builder.yml`, `desktop/src/index.js`, `desktop/src/tray.js`, `desktop/src/webui/index.js`, `desktop/src/auto-launch.js`: use Space Data Network labels/app IDs where desktop shell surfaces still say IPFS Desktop.

### Tasks

- [x] Add failing source tests for primary and toolbar logo contracts.
- [x] Replace SDN SVG sources with the approved primary and toolbar marks.
- [x] Regenerate desktop and web raster icon assets.
- [x] Update desktop product labels from IPFS Desktop to Space Data Network on shell-visible surfaces.
- [x] Add the logo story to project docs.
- [x] Update the desktop intro page buttons to square IPFS-style controls and correct `spacedatanetwork.org`.
- [x] Register `sdn://` and `webui://` together as fetch-capable privileged Electron schemes.
- [x] Run focused source/unit tests.
- [x] Rebuild `sdn-js` UI.
- [x] Rebuild SDN Desktop.
- [x] Launch SDN Desktop locally.
- [x] Run required stack status checks.
