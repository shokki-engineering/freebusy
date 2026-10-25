-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "resource" table
ALTER TABLE "reservation"."resource" ADD CONSTRAINT "chk_resource_last_change" CHECK (last_change = ANY (ARRAY['STATE'::text, 'DETAILS'::text])), ADD COLUMN "last_change" text NULL DEFAULT 'STATE';
-- Create index "idx_resource_customer" to table: "resource"
CREATE INDEX "idx_resource_customer" ON "reservation"."resource" ("customer");
