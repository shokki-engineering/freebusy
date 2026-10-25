-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "resource" table
ALTER TABLE "event"."resource" ADD CONSTRAINT "chk_resource_original_start_range" CHECK (original_start_range = 'THIS_AND_FUTURE'::text), ADD COLUMN "original_start_range" text NULL;
