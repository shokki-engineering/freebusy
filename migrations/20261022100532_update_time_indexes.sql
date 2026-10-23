-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Create index "idx_availabilities_update_time" to table: "availabilities"
CREATE INDEX "idx_availabilities_update_time" ON "availability"."availabilities" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "calendar"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "card"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "event"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "journal"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "ldapgroup"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "membership"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "organization"."resource" ("update_time");
-- Create index "idx_organizational_units_update_time" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_update_time" ON "orgunit"."organizational_units" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "pack"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "project"."resource" ("update_time");
-- Create index "idx_entity_update_time" to table: "entity"
CREATE INDEX "idx_entity_update_time" ON "resource"."entity" ("update_time");
-- Create index "idx_groups_update_time" to table: "groups"
CREATE INDEX "idx_groups_update_time" ON "scimgroup"."groups" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "sensor"."resource" ("update_time");
-- Create index "idx_idempotency_keys_update_time" to table: "idempotency_keys"
CREATE INDEX "idx_idempotency_keys_update_time" ON "shared"."idempotency_keys" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "stream"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "todo"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "unit"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "user"."resource" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "vcard"."resource" ("update_time");
-- Create index "idx_deliveries_update_time" to table: "deliveries"
CREATE INDEX "idx_deliveries_update_time" ON "webhook"."deliveries" ("update_time");
-- Create index "idx_feed_cursors_update_time" to table: "feed_cursors"
CREATE INDEX "idx_feed_cursors_update_time" ON "webhook"."feed_cursors" ("update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "webhook"."resource" ("update_time");
