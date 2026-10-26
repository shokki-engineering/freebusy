-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Drop schema named "ldapgroup"
DROP SCHEMA "ldapgroup" CASCADE;
-- Drop schema named "membership"
DROP SCHEMA "membership" CASCADE;
-- Drop schema named "organization"
DROP SCHEMA "organization" CASCADE;
-- Drop schema named "orgunit"
DROP SCHEMA "orgunit" CASCADE;
-- Modify "resource_days" table
ALTER TABLE "allocation"."resource_days" DROP COLUMN "organizational_unit";
