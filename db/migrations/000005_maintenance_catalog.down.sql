-- Only the global catalogue. A user's custom items are their data, not seed.
DELETE FROM maintenance_items WHERE owner_user_id IS NULL;
