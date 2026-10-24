-- Queries now use the stored preferences.timezone as is (no pg_timezone_names
-- check per query); the API only saves real zone names. Reset any name
-- Postgres doesn't know to the default, once.
UPDATE user_preferences
SET preferences = jsonb_set(preferences, '{timezone}', '"Asia/Bangkok"')
WHERE preferences ? 'timezone'
  AND (preferences->>'timezone' = ''
       OR preferences->>'timezone' NOT IN (SELECT name FROM pg_timezone_names));
