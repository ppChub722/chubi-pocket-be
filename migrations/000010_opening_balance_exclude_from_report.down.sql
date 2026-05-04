UPDATE categories
   SET include_in_report = TRUE
 WHERE is_system = TRUE
   AND system_kind IN ('OPENING_IN', 'OPENING_OUT');
