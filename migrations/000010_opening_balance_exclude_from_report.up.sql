-- Spec §03/§2.7 + §05/§4.14c: per-account summaries respect each
-- transaction's category.include_in_report flag. Opening Balance rows
-- were originally seeded with include_in_report = TRUE under the
-- assumption "real money flowing in/out". In practice users see this
-- as bookkeeping, not earning, so it inflates "income this period".
--
-- This migration flips the OPENING_IN / OPENING_OUT rows for every
-- existing user. New users get the right value via the seed in
-- categories/seed.go.

UPDATE categories
   SET include_in_report = FALSE
 WHERE is_system = TRUE
   AND system_kind IN ('OPENING_IN', 'OPENING_OUT');
