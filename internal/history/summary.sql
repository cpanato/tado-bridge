-- Shared by the history API and read-only Grafana queries. Values between
-- collections are held only up to max_gap_seconds; missing fields stay unknown.
CREATE OR REPLACE FUNCTION tado_bridge_daily_summary(
    range_from timestamptz, range_to timestamptz,
    zone_name text DEFAULT 'UTC', max_gap_seconds double precision DEFAULT 7200,
    home_filter text DEFAULT '', room_filter text DEFAULT '',
    retain_after timestamptz DEFAULT '-infinity'
) RETURNS SETOF jsonb LANGUAGE sql STABLE AS $summary$
WITH bounds AS (
    SELECT range_from AS first, least(range_to, statement_timestamp()) AS last
    WHERE range_from < range_to AND range_to - range_from <= interval '31 days'
      AND max_gap_seconds BETWEEN 60 AND 86400
), source AS (
    SELECT s.*, lead(s.collected_at) OVER (ORDER BY s.collected_at) AS next_at
    FROM bounds b, tado_bridge_snapshots s
    WHERE s.collected_at >= greatest(b.first - make_interval(secs => max_gap_seconds), retain_after)
      AND s.collected_at < b.last
), readings AS (
    SELECT s.collected_at AS at, h->>'id' AS home_id, z->>'id' AS zone_id,
           h->>'name' AS home_name, z->>'name' AS zone_name,
           (z->>'temperature_c')::double precision AS temperature,
           (z->>'target_temperature_c')::double precision AS target,
           (z->>'humidity_percent')::double precision AS humidity,
           (z->>'heating_active')::boolean AS heating, s.next_at
    FROM source s,
         jsonb_array_elements(s.payload->'homes') h,
         jsonb_array_elements(h->'zones') z
    WHERE (home_filter = '' OR h->>'id' = home_filter)
      AND (room_filter = '' OR z->>'id' = room_filter)
), segments AS (
    SELECT r.*, least(coalesce(next_at, b.last), at + make_interval(secs => max_gap_seconds), b.last) AS until
    FROM readings r CROSS JOIN bounds b
), days AS (
    SELECT d AT TIME ZONE zone_name AS day_start,
           (d + interval '1 day') AT TIME ZONE zone_name AS day_end,
           to_char(d, 'YYYY-MM-DD') AS date,
           greatest(d AT TIME ZONE zone_name, b.first) AS first,
           least((d + interval '1 day') AT TIME ZONE zone_name, b.last) AS last
    FROM bounds b CROSS JOIN LATERAL generate_series(
        date_trunc('day', b.first AT TIME ZONE zone_name),
        date_trunc('day', (b.last - interval '1 microsecond') AT TIME ZONE zone_name),
        interval '1 day') d
), spans AS (
    SELECT d.date, d.day_start, s.*,
           s.at >= d.first AND s.at < d.last AS sampled,
           greatest(0, extract(epoch FROM least(s.until, d.last) - greatest(s.at, d.first)))::double precision AS seconds
    FROM days d JOIN segments s ON s.at < d.last AND s.until > d.first
), daily AS (
    SELECT date, day_start, home_id, zone_id,
           (array_agg(home_name ORDER BY at DESC))[1] AS home_name,
           (array_agg(zone_name ORDER BY at DESC))[1] AS zone_name,
           min(temperature) FILTER (WHERE sampled) AS t_min,
           max(temperature) FILTER (WHERE sampled) AS t_max,
           avg(temperature) FILTER (WHERE sampled) AS t_mean,
           count(temperature) FILTER (WHERE sampled) AS t_samples,
           min(target) FILTER (WHERE sampled) AS target_min,
           max(target) FILTER (WHERE sampled) AS target_max,
           avg(target) FILTER (WHERE sampled) AS target_mean,
           count(target) FILTER (WHERE sampled) AS target_samples,
           min(humidity) FILTER (WHERE sampled) AS h_min,
           max(humidity) FILTER (WHERE sampled) AS h_max,
           avg(humidity) FILTER (WHERE sampled) AS h_mean,
           count(humidity) FILTER (WHERE sampled) AS h_samples,
           coalesce(sum(seconds) FILTER (WHERE temperature < target), 0) AS below_seconds,
           coalesce(sum(seconds) FILTER (WHERE temperature IS NOT NULL AND target IS NOT NULL), 0) AS target_seconds,
           coalesce(sum(seconds) FILTER (WHERE heating), 0) AS heating_seconds,
           coalesce(sum(seconds) FILTER (WHERE heating IS NOT NULL), 0) AS heating_known_seconds
    FROM spans GROUP BY date, day_start, home_id, zone_id
)
SELECT jsonb_build_object(
    'date', date, 'day_start', day_start,
    'home_id', home_id, 'zone_id', zone_id, 'home_name', home_name, 'zone_name', zone_name,
    'temperature_c', jsonb_build_object('min', t_min, 'max', t_max, 'mean', t_mean, 'samples', t_samples),
    'target_temperature_c', jsonb_build_object('min', target_min, 'max', target_max, 'mean', target_mean, 'samples', target_samples),
    'humidity_percent', jsonb_build_object('min', h_min, 'max', h_max, 'mean', h_mean, 'samples', h_samples),
    'below_target_seconds', below_seconds, 'target_coverage_seconds', target_seconds,
    'heating_active_seconds', heating_seconds, 'heating_coverage_seconds', heating_known_seconds,
    'temperature_mean_change_c', CASE WHEN
        lag(date::date) OVER w = date::date - 1
        THEN t_mean - lag(t_mean) OVER w END
)
FROM daily WINDOW w AS (PARTITION BY home_id, zone_id ORDER BY date)
ORDER BY date, home_id, zone_id
LIMIT 4097;
$summary$;
