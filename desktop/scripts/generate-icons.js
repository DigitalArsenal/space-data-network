#!/usr/bin/env node
/**
 * SDN Icon Generator
 *
 * This script documents the icon assets and provides instructions for
 * generating PNG/ICNS/ICO files from the master SVG.
 *
 * Master SVG: desktop/assets/build/icon.svg
 * Tray SVG:   desktop/assets/icons/tray/sdn-tray.svg
 * Splash SVG:  desktop/assets/pages/sdn-splash.svg
 *
 * === Generating PNGs from SVG ===
 *
 * Option 1: Use the icon-generator.html file
 *   Open desktop/scripts/icon-generator.html in a browser.
 *   It renders the SVG at multiple sizes and lets you right-click save each PNG.
 *
 * Option 2: Use sharp (if installed)
 *   npm install sharp
 *   node -e "
 *     const sharp = require('sharp');
 *     const fs = require('fs');
 *     const svg = fs.readFileSync('desktop/assets/build/icon.svg');
 *     [16, 22, 32, 44, 64, 66, 128, 256, 512, 1024].forEach(size => {
 *       sharp(svg).resize(size, size).png().toFile(\`icon-\${size}.png\`);
 *     });
 *   "
 *
 * Option 3: Use Inkscape CLI (if installed)
 *   inkscape -w 1024 -h 1024 icon.svg -o icon-1024.png
 *
 * Option 4: Use rsvg-convert (librsvg, available via brew)
 *   brew install librsvg
 *   rsvg-convert -w 1024 -h 1024 icon.svg > icon-1024.png
 *
 * === Generating .icns (macOS) ===
 *   mkdir icon.iconset
 *   for size in 16 32 64 128 256 512 1024; do
 *     rsvg-convert -w $size -h $size icon.svg > icon.iconset/icon_${size}x${size}.png
 *   done
 *   # Also need @2x variants
 *   cp icon.iconset/icon_32x32.png icon.iconset/icon_16x16@2x.png
 *   cp icon.iconset/icon_64x64.png icon.iconset/icon_32x32@2x.png
 *   cp icon.iconset/icon_256x256.png icon.iconset/icon_128x128@2x.png
 *   cp icon.iconset/icon_512x512.png icon.iconset/icon_256x256@2x.png
 *   cp icon.iconset/icon_1024x1024.png icon.iconset/icon_512x512@2x.png
 *   iconutil -c icns icon.iconset -o icon.icns
 *
 * === Generating .ico (Windows) ===
 *   Use ImageMagick:
 *   convert icon-16.png icon-32.png icon-48.png icon-64.png icon-128.png icon-256.png icon.ico
 *
 * === Generating tray template PNGs (macOS) ===
 *   The tray icons use white-only artwork (macOS "Template" images).
 *   Source: desktop/assets/icons/tray/sdn-tray.svg
 *
 *   rsvg-convert -w 22 -h 22 sdn-tray.svg > on-22Template.png
 *   rsvg-convert -w 44 -h 44 sdn-tray.svg > on-22Template@2x.png
 *   rsvg-convert -w 66 -h 66 sdn-tray.svg > on-22Template@3x.png
 *
 *   For the "off" state, you can reduce opacity or use a dimmed variant.
 */

const fs = require('fs')
const path = require('path')

const SIZES = [16, 22, 32, 44, 48, 64, 66, 128, 256, 512, 1024]

// Read master SVG
const svgPath = path.join(__dirname, '..', 'assets', 'build', 'icon.svg')
const traySvgPath = path.join(__dirname, '..', 'assets', 'icons', 'tray', 'sdn-tray.svg')

if (!fs.existsSync(svgPath)) {
  console.error('Master SVG not found at', svgPath)
  process.exit(1)
}

console.log('SDN Icon Assets')
console.log('================')
console.log('')
console.log('Master app icon SVG:', svgPath)
console.log('Tray icon SVG:', traySvgPath)
console.log('')
console.log('Required PNG sizes:', SIZES.join(', '))
console.log('')

// Try sharp if available
try {
  const sharp = require('sharp')
  const svg = fs.readFileSync(svgPath)
  const outDir = path.join(__dirname, '..', 'assets', 'build')

  console.log('sharp found! Generating PNGs...')

  Promise.all(SIZES.map(size =>
    sharp(svg)
      .resize(size, size)
      .png()
      .toFile(path.join(outDir, `icon-${size}.png`))
      .then(() => console.log(`  Generated icon-${size}.png`))
  )).then(() => {
    console.log('')
    console.log('Done! PNGs written to', outDir)
    console.log('')
    console.log('To generate .icns and .ico, see instructions at top of this script.')
  }).catch(err => {
    console.error('Error generating PNGs:', err.message)
  })
} catch (e) {
  console.log('sharp not found. To generate PNGs programmatically:')
  console.log('  npm install sharp')
  console.log('  node desktop/scripts/generate-icons.js')
  console.log('')
  console.log('Or open desktop/scripts/icon-generator.html in a browser.')
}
