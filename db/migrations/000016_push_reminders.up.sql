-- Push reminders (SPEC.md D-19): the phones that asked to be reminded, and what each
-- person has already been told.
--
-- Neither table stores a status or a due date. Whether something is late is still derived
-- on every read (RN-06, RN-06b); notification_log only answers "was this person already
-- told about this item at this stage?", which is the one thing the clock cannot re-derive.

-- An FCM registration token names an installation of the app, not a person. When someone
-- signs out and another account signs in on the same phone, the token stays the same and
-- the row moves to the new owner, which is why the token is unique and not the pair.
CREATE TABLE push_devices (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token        text        NOT NULL UNIQUE,
    platform     text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    -- The last time the app confirmed the token. FCM retires tokens that go unused, and
    -- this is what a future sweep would tell a living phone from a forgotten one by.
    last_seen_at timestamptz NOT NULL DEFAULT now(),

    -- Android only for now: iOS needs an APNs key and a build this project cannot make yet.
    CONSTRAINT push_devices_platform_check CHECK (platform IN ('android')),
    CONSTRAINT push_devices_token_check CHECK (length(token) BETWEEN 1 AND 4096)
);

CREATE INDEX push_devices_user_id_idx ON push_devices (user_id);

-- One row per reminder that went out. The unique key is the deduplication: the job wakes
-- several times in the sending hour and would find the same overdue IPVA every time, and
-- it runs again after every deploy. A timer in memory would not survive either.
--
-- kind is text, not an enum, on purpose: adding a reminder must not need a migration, and
-- this column decides nothing — it only records what was said.
--
-- marker names the due point the reminder was about ("2026-10-01|", "|45000"). A plan's id
-- survives from one cycle to the next; the due point does not, so recording the next oil
-- change reopens the reminder for the change after it.
CREATE TABLE notification_log (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    vehicle_id uuid        NOT NULL REFERENCES vehicles (id) ON DELETE CASCADE,
    kind       text        NOT NULL,
    subject_id uuid        NOT NULL,
    marker     text        NOT NULL,
    -- The civil day in America/Sao_Paulo it went out: the "one push per car per day" rule
    -- is asked of this column.
    sent_on    date        NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),

    CONSTRAINT notification_log_once UNIQUE (user_id, kind, subject_id, marker)
);

CREATE INDEX notification_log_vehicle_day_idx ON notification_log (user_id, vehicle_id, sent_on);
