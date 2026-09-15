-- Projects gain a nullable planned_amount — plan-vs-actual single total.
-- Spec: design/spec/10-projects.md §4.23. NULL = feature off (plan UI
-- hidden). Display-only: no validation anywhere references it.

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS planned_amount DECIMAL(15,2)
        CHECK (planned_amount > 0);
