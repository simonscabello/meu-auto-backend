-- What the owner told us about how their car is built.
--
-- Small on purpose. This table exists for the one thing plans cannot express: that somebody
-- was asked and answered "não sei". Without it the app has no way to tell "never asked"
-- from "asked, and they do not know", so it asks forever.

-- name: UpsertVehicleProfileAnswer :one
INSERT INTO vehicle_profile_answers (vehicle_id, question, answer, source)
VALUES ($1, $2, $3, $4)
ON CONFLICT (vehicle_id, question) DO UPDATE
SET answer      = EXCLUDED.answer,
    source      = EXCLUDED.source,
    answered_at = now()
RETURNING *;

-- name: ListVehicleProfileAnswers :many
SELECT * FROM vehicle_profile_answers
WHERE vehicle_id = $1
ORDER BY question;
