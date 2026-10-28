-- 0.4.1-2 V12 (Q-E2): after an event is completed, each member may book the "fewest transfers"
-- (simplified) settle into their own book once ("ลงในสมุดฉัน"): a plain transaction per simplified
-- line with them + person-book rows without a transaction that clear the pairs they had claimed. The
-- row marks it done: once booked, that member's real pairs can't be claimed any more.
CREATE TABLE event_settle_bookings (
    project_id UUID NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    lines      JSONB NOT NULL DEFAULT '[]'::jsonb,
    booked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_id, user_id)
);
