-- Registers or reconfirms a phone.
--
-- An existing token CHANGES OWNER instead of duplicating: FCM delivers to an installation,
-- not to an account, and when another person signs in on the same phone the reminders
-- about the previous owner's car must stop going there.
-- name: UpsertDevice :exec
INSERT INTO push_devices (user_id, token, platform)
VALUES ($1, $2, $3)
ON CONFLICT (token) DO UPDATE
SET user_id      = EXCLUDED.user_id,
    platform     = EXCLUDED.platform,
    last_seen_at = now();

-- Forgets a phone. Only the caller's own row: a token is not a credential, and knowing one
-- must not let anybody unsubscribe somebody else's phone.
-- name: DeleteDeviceForUser :execrows
DELETE FROM push_devices
WHERE token = $1 AND user_id = $2;

-- A token FCM reported as gone (uninstalled app, cleared data, token rotated).
-- name: DeleteDeviceByToken :exec
DELETE FROM push_devices
WHERE token = $1;

-- name: ListDeviceTokensForUser :many
SELECT token
FROM push_devices
WHERE user_id = $1
ORDER BY created_at;

-- Everybody a reminder could reach. Without a phone there is nobody to tell, so the job
-- never looks at an account that has none.
-- name: ListUsersWithDevices :many
SELECT DISTINCT user_id
FROM push_devices;
