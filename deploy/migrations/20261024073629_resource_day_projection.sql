-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "holds" table
ALTER TABLE "allocation"."holds" ADD COLUMN "candidate_filter" text NULL;
-- Create "resource_days" table
CREATE TABLE "allocation"."resource_days" ("id" text NOT NULL, "resource" text NOT NULL, "organization" text NULL, "organizational_unit" text NULL, "day" date NOT NULL, "free" tstzmultirange NOT NULL, "resource_types" text[] NULL, "attributes" jsonb NULL, "calendar" text NULL, "display_name" text NULL, "free_fraction" double precision NULL, "row_version" bigint NOT NULL DEFAULT 0, PRIMARY KEY ("id"));
-- Create index "idx_resource_days_attributes_gin" to table: "resource_days"
CREATE INDEX "idx_resource_days_attributes_gin" ON "allocation"."resource_days" USING gin ("attributes");
-- Create index "idx_resource_days_calendar" to table: "resource_days"
CREATE INDEX "idx_resource_days_calendar" ON "allocation"."resource_days" ("calendar");
-- Create index "idx_resource_days_display_name_trgm" to table: "resource_days"
CREATE INDEX "idx_resource_days_display_name_trgm" ON "allocation"."resource_days" USING gin ("display_name" gin_trgm_ops);
-- Create index "idx_resource_days_free_gist" to table: "resource_days"
CREATE INDEX "idx_resource_days_free_gist" ON "allocation"."resource_days" USING gist ("free");
-- Create index "idx_resource_days_organization_day" to table: "resource_days"
CREATE INDEX "idx_resource_days_organization_day" ON "allocation"."resource_days" ("organization", "day");
-- Create index "idx_resource_days_organizational_unit_day" to table: "resource_days"
CREATE INDEX "idx_resource_days_organizational_unit_day" ON "allocation"."resource_days" ("organizational_unit", "day");
-- Create index "idx_resource_days_resource_day" to table: "resource_days"
CREATE UNIQUE INDEX "idx_resource_days_resource_day" ON "allocation"."resource_days" ("resource", "day");
-- Create index "idx_resource_days_resource_types_gin" to table: "resource_days"
CREATE INDEX "idx_resource_days_resource_types_gin" ON "allocation"."resource_days" USING gin ("resource_types");
