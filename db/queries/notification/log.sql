-- Records that a reminder is going out, reporting 0 rows when it already went out before.
--
-- Written BEFORE the push is sent. If the process dies between the two, one reminder is
-- lost — far better than the alternative: a job that repeats itself every ten minutes.
-- name: RecordReminder :execrows
INSERT INTO notification_log (user_id, vehicle_id, kind, subject_id, marker, sent_on)
VALUES ($1, $2, $3, $4, $5, $6)
ON CONFLICT (user_id, kind, subject_id, marker) DO NOTHING;

-- Whether this person already got a reminder about this car today. One push per car per
-- day: what becomes due after the morning's push waits for tomorrow's.
-- name: VehicleRemindedOn :one
SELECT EXISTS (
    SELECT 1
    FROM notification_log
    WHERE user_id = $1 AND vehicle_id = $2 AND sent_on = $3
);
