-- L07-admission.md's Future work: rule 5's dailySpent counter was
-- process-lifetime-only, resetting on every restart — strictly more
-- permissive than the real 24-hour window 02-config.md describes. One
-- row per calendar day (local time — this is a single-user tool tracking
-- "your card", per 02-config.md's own framing, not a fleet spanning
-- timezones), upserted after every admitted request that carries an
-- EstimatedCostUSD, so a restart mid-day resumes the same day's total
-- instead of starting over at zero.
CREATE TABLE admission_spend (
  day TEXT PRIMARY KEY,   -- "2026-08-21", local time
  spent_usd REAL NOT NULL
) STRICT;
