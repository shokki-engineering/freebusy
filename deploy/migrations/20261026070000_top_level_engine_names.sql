-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Holds, allocations and reservations are top-level collections now: their
-- names, and the names other rows keep of them, lose the
-- "organizationalUnits/{organizational_unit}/" prefix they had in 1.0.0-rc.1.
-- Written by hand: this rewrites data, which no generated diff does.
--
-- Two sites could have used one id, which would collide once the prefix goes.
-- That is refused before anything is written, naming the collection, so the
-- duplicates can be renamed or removed and the migration run again.
DO $$
DECLARE
  clash text;
BEGIN
  SELECT 'holds' INTO clash FROM allocation.holds
    GROUP BY regexp_replace(name, '^organizationalUnits/[^/]+/', '') HAVING count(*) > 1 LIMIT 1;
  IF clash IS NULL THEN
    SELECT 'allocations' INTO clash FROM allocation.resource
      GROUP BY regexp_replace(name, '^organizationalUnits/[^/]+/', '') HAVING count(*) > 1 LIMIT 1;
  END IF;
  IF clash IS NULL THEN
    SELECT 'reservations' INTO clash FROM reservation.resource
      GROUP BY regexp_replace(name, '^organizationalUnits/[^/]+/', '') HAVING count(*) > 1 LIMIT 1;
  END IF;
  IF clash IS NOT NULL THEN
    RAISE EXCEPTION 'two sites share an id among % -- rename or remove one of each pair, then migrate again', clash;
  END IF;
END $$;
-- Rename "holds" rows and their replaced hold
UPDATE "allocation"."holds"
  SET "name" = regexp_replace("name", '^organizationalUnits/[^/]+/', ''),
      "replaces" = regexp_replace("replaces", '^organizationalUnits/[^/]+/', '')
  WHERE "name" LIKE 'organizationalUnits/%' OR "replaces" LIKE 'organizationalUnits/%';
-- Rename "resource" (allocation) rows and their hold
UPDATE "allocation"."resource"
  SET "name" = regexp_replace("name", '^organizationalUnits/[^/]+/', ''),
      "hold" = regexp_replace("hold", '^organizationalUnits/[^/]+/', '')
  WHERE "name" LIKE 'organizationalUnits/%' OR "hold" LIKE 'organizationalUnits/%';
-- Rename "resource" (reservation) rows and the holds and reservations they name
UPDATE "reservation"."resource"
  SET "name" = regexp_replace("name", '^organizationalUnits/[^/]+/', ''),
      "hold" = regexp_replace("hold", '^organizationalUnits/[^/]+/', ''),
      "replaces" = regexp_replace("replaces", '^organizationalUnits/[^/]+/', ''),
      "replacement" = regexp_replace("replacement", '^organizationalUnits/[^/]+/', '')
  WHERE "name" LIKE 'organizationalUnits/%' OR "hold" LIKE 'organizationalUnits/%'
     OR "replaces" LIKE 'organizationalUnits/%' OR "replacement" LIKE 'organizationalUnits/%';
