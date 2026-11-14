-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- A hold's retry key moved to CreateHoldRequest.request_id (AIP-155), kept by
-- the idempotency interceptor in shared.idempotency_keys, and ResourceDay.free
-- is free_intervals (AIP-140). The column is renamed, not dropped and added as
-- the generated diff has it: the diff's NOT NULL column cannot be added to a
-- projection that has rows, and a rename keeps them. Written by hand for that.
-- Modify "holds" table
ALTER TABLE "allocation"."holds" DROP COLUMN "idempotency_key";
-- Modify "resource_days" table
ALTER TABLE "allocation"."resource_days" RENAME COLUMN "free" TO "free_intervals";
-- Rename index "idx_resource_days_free_gist" to "idx_resource_days_free_intervals_gist"
ALTER INDEX "allocation"."idx_resource_days_free_gist" RENAME TO "idx_resource_days_free_intervals_gist";
