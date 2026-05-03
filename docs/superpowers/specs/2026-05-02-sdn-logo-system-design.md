# SDN Logo System Design

## Goal

Replace the SDN logo across SDN-owned project surfaces with a simple monochrome mark that is readable in the system toolbar and consistent across desktop, web, and package branding.

## Approved Mark

The primary SDN mark is a transparent-background, single-color symbol:

- circle outline
- inverted triangle outline touching the circle
- centered dot

Use white on dark surfaces. Use black on light surfaces. Do not use red, mixed-color strokes, tiny SDN letters, orbit lines, or filled background badges.

The desktop system toolbar mark is a simplified companion using the same center and triangle direction:

- solid inverted triangle
- centered dot cut out of the triangle
- transparent background

This is intentionally simpler than the primary mark because menu-bar icons render at very small sizes and need to work as relief/template assets.

## Logo Story

The circle is the shared orbital commons: the boundary of a trusted network that has no single center of control. The inverted triangle is the downlink: space data moving from orbit into usable, verified systems on the ground. The dot is the datum and the node at the same time: one observation, one peer, one signed fact in the network.

Together, the mark says: SDN turns space data into a verifiable signal. The toolbar version reduces that story to its most durable silhouette, a downlink triangle with a missing center point that reads as a signal aperture in relief.

## Scope

Update SDN-owned logo assets in `repos/main-packages/space-data-network`:

- SDN root/admin UI navigation logo.
- Upstream-style web UI logo files and favicon assets where the SDN package still serves them.
- Desktop app build icon, splash logo, and tray/system-toolbar assets.
- Desktop-visible product labels that still say IPFS Desktop in the SDN shell.

Do not edit third-party logos, upstream documentation examples, Cesium/OrbPro product favicons, Basilisk logos, Maki map icons, or migration-debt nested copies inside other component repos.

## Verification

- Source tests assert the SVG contracts for the shared mark and toolbar mark.
- Desktop unit/source tests assert the desktop product labels and toolbar asset paths.
- Regenerate PNG, ICO, and ICNS assets from the SVG sources.
- Rebuild SDN JS UI because web/admin logo assets ship through that bundle.
- Rebuild SDN Desktop and run it locally on this machine after the asset update.
