-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Create index "idx_holds_state_expire_time" to table: "holds"
CREATE INDEX "idx_holds_state_expire_time" ON "allocation"."holds" ("state", "expire_time");
-- Create index "idx_holds_update_time" to table: "holds"
CREATE INDEX "idx_holds_update_time" ON "allocation"."holds" ("update_time");
-- Create index "idx_resource_state_update_time" to table: "resource"
CREATE INDEX "idx_resource_state_update_time" ON "reservation"."resource" ("state", "update_time");
-- Create index "idx_resource_update_time" to table: "resource"
CREATE INDEX "idx_resource_update_time" ON "reservation"."resource" ("update_time");
