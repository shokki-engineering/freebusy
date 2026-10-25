-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "holds" table
ALTER TABLE "allocation"."holds" ADD COLUMN "replaces" text NULL;
