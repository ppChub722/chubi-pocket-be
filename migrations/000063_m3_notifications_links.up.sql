-- 0.4.0 M3: event notifications link to the event page (the app has no
-- per-bill route): /projects/<id>/transactions/<bill> -> /projects/<id>.
UPDATE notifications
SET deep_link = regexp_replace(deep_link, '^(/projects/[^/]+)/transactions/.*$', '\1')
WHERE deep_link LIKE '/projects/%/transactions/%';
