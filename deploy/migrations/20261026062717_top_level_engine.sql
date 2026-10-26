-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "holds" table
ALTER TABLE "allocation"."holds" DROP COLUMN "organizational_unit_id";
-- Modify "resource" table
ALTER TABLE "allocation"."resource" DROP COLUMN "organizational_unit_id";
-- Modify "resource_days" table
ALTER TABLE "allocation"."resource_days" DROP COLUMN "organization";
-- Create index "idx_resource_days_day_resource_id" to table: "resource_days"
CREATE INDEX "idx_resource_days_day_resource_id" ON "allocation"."resource_days" ("day", "resource", "id");
-- Modify "resource" table
ALTER TABLE "reservation"."resource" DROP COLUMN "organizational_unit_id";
-- Modify "resource" table
ALTER TABLE "webhook"."resource" DROP COLUMN "organizational_unit";
