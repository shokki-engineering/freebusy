-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Validate "fk_holds_organizational_unit_id" on "allocation"."holds"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_holds_organizational_unit_id' AND conrelid = to_regclass('"allocation"."holds"') AND NOT convalidated) THEN
    ALTER TABLE "allocation"."holds" VALIDATE CONSTRAINT "fk_holds_organizational_unit_id";
  END IF;
END $$;

-- Validate "fk_resource_organizational_unit_id" on "allocation"."resource"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_resource_organizational_unit_id' AND conrelid = to_regclass('"allocation"."resource"') AND NOT convalidated) THEN
    ALTER TABLE "allocation"."resource" VALIDATE CONSTRAINT "fk_resource_organizational_unit_id";
  END IF;
END $$;

-- Validate "fk_resource_resource" on "allocation"."resource"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_resource_resource' AND conrelid = to_regclass('"allocation"."resource"') AND NOT convalidated) THEN
    ALTER TABLE "allocation"."resource" VALIDATE CONSTRAINT "fk_resource_resource";
  END IF;
END $$;

-- Validate "fk_resource_organization" on "ldapgroup"."resource"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_resource_organization' AND conrelid = to_regclass('"ldapgroup"."resource"') AND NOT convalidated) THEN
    ALTER TABLE "ldapgroup"."resource" VALIDATE CONSTRAINT "fk_resource_organization";
  END IF;
END $$;

-- Validate "fk_organizational_units_organization" on "orgunit"."organizational_units"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_organizational_units_organization' AND conrelid = to_regclass('"orgunit"."organizational_units"') AND NOT convalidated) THEN
    ALTER TABLE "orgunit"."organizational_units" VALIDATE CONSTRAINT "fk_organizational_units_organization";
  END IF;
END $$;

-- Validate "fk_resource_organizational_unit_id" on "reservation"."resource"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_resource_organizational_unit_id' AND conrelid = to_regclass('"reservation"."resource"') AND NOT convalidated) THEN
    ALTER TABLE "reservation"."resource" VALIDATE CONSTRAINT "fk_resource_organizational_unit_id";
  END IF;
END $$;

-- Validate "fk_resource_organizational_unit" on "webhook"."resource"
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'fk_resource_organizational_unit' AND conrelid = to_regclass('"webhook"."resource"') AND NOT convalidated) THEN
    ALTER TABLE "webhook"."resource" VALIDATE CONSTRAINT "fk_resource_organizational_unit";
  END IF;
END $$;
