-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

CREATE OR REPLACE FUNCTION allocation.resource_resource_no_overlap_lock()
 RETURNS trigger
 LANGUAGE plpgsql
AS $function$
BEGIN
  PERFORM pg_advisory_xact_lock(hashtextextended('resource_resource_no_overlap' || chr(31) || coalesce(NEW."resource"::text, ''), 0));
  RETURN NEW;
END $function$;

DROP TRIGGER IF EXISTS "resource_resource_no_overlap_lock" ON allocation.resource;
CREATE TRIGGER resource_resource_no_overlap_lock BEFORE INSERT OR UPDATE OF resource, start_time, end_time ON allocation.resource FOR EACH ROW EXECUTE FUNCTION allocation.resource_resource_no_overlap_lock();
