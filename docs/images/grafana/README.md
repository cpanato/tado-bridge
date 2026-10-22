# Grafana preview image

`dashboard.png` is a screenshot of the real Grafana UI using only generated climate readings for eight generic rooms. The top gauges use the automatically discovered **Current room** selection and identify that room in their titles. It includes real Prometheus scrape results and PostgreSQL panel queries against a disposable local Compose stack; there are no OAuth credentials, real home names/IDs or private account readings.

Regenerate with `make release-snapshot` followed by `make observability-screenshot`. Docker, Node 22+ and Chrome are required. `NODE`/`CHROME_BIN` can select local binaries. The capture helper uses Node built-ins and Chrome DevTools Protocol, with an isolated browser profile that is removed afterward. All Compose test state is also removed. Inspect the full screenshot after template changes; the image documents synthetic behavior rather than proving availability of any upstream account or physical display.
