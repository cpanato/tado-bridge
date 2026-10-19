# TRMNL preview images

These four PNGs are local browser renders of `integrations/trmnl/*.liquid` using `integrations/trmnl/example-payload.json`. Every name and reading is synthetic, including the garage's zero and unavailable target/humidity/heating values. No tado credentials, webhook URLs, home/zone IDs or authenticated account screens are used.

Rendered with LiquidJS 10.30.0 (official npm registry SHA-512 verified), Chrome headless and [TRMNL's official stylesheet](https://trmnl.com/css/latest/plugins.css), checked on 2026-10-19. For fresh-state screenshots, collection time is set to capture time in memory; the checked-in payload keeps a fixed example timestamp for stale-state testing. The HTML uses the official `trmnl`, `screen` and corresponding `view--*` viewport classes; images are captured directly at full 800×480, horizontal half 800×240, vertical half 400×480 and quadrant 400×240 sizes.

Regenerate the images when templates change. Validate null/zero handling, escaped aliases, visible room limits, empty cache and fresh/stale states during Liquid rendering; then inspect all four viewport screenshots. These are browser previews, not proof of identical physical e-paper rendering. The runtime binary and CI tests require no Node or browser dependency.
