-- Down migrations are written so a mistake can be undone locally. They are never run
-- against production (SPEC.md D-05).
--
-- Dropping an extension fails while any column still depends on it, which is the correct
-- behaviour: it means a later migration must be reverted first.

DROP EXTENSION IF EXISTS pgcrypto;
DROP EXTENSION IF EXISTS citext;
