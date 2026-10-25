-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Drop index "idx_resource_days_calendar" from table: "resource_days"
DROP INDEX "allocation"."idx_resource_days_calendar";
-- Drop index "idx_resource_days_organization_day" from table: "resource_days"
DROP INDEX "allocation"."idx_resource_days_organization_day";
-- Drop index "idx_resource_days_organizational_unit_day" from table: "resource_days"
DROP INDEX "allocation"."idx_resource_days_organizational_unit_day";
-- Create index "idx_resource_days_calendar_id" to table: "resource_days"
CREATE INDEX "idx_resource_days_calendar_id" ON "allocation"."resource_days" ("calendar", "id");
-- Create index "idx_resource_days_organization_day_id" to table: "resource_days"
CREATE INDEX "idx_resource_days_organization_day_id" ON "allocation"."resource_days" ("organization", "day", "id");
-- Create index "idx_resource_days_organization_day_organizational_unit_resource" to table: "resource_days"
CREATE INDEX "idx_resource_days_organization_day_organizational_unit_resource" ON "allocation"."resource_days" ("organization", "day", "organizational_unit", "resource", "id");
-- Create index "idx_resource_days_organizational_unit_day_id" to table: "resource_days"
CREATE INDEX "idx_resource_days_organizational_unit_day_id" ON "allocation"."resource_days" ("organizational_unit", "day", "id");
