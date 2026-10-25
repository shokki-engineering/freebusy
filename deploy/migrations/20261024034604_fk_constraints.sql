-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Modify "holds" table
ALTER TABLE "allocation"."holds" ADD CONSTRAINT "fk_holds_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "resource" table
ALTER TABLE "allocation"."resource" ADD CONSTRAINT "fk_resource_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID, ADD CONSTRAINT "fk_resource_resource" FOREIGN KEY ("resource") REFERENCES "resource"."entity" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "resource" table
ALTER TABLE "ldapgroup"."resource" ADD CONSTRAINT "fk_resource_organization" FOREIGN KEY ("organization") REFERENCES "organization"."resource" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "organizational_units" table
ALTER TABLE "orgunit"."organizational_units" ADD CONSTRAINT "fk_organizational_units_organization" FOREIGN KEY ("organization") REFERENCES "organization"."resource" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "resource" table
ALTER TABLE "reservation"."resource" ADD CONSTRAINT "fk_resource_organizational_unit_id" FOREIGN KEY ("organizational_unit_id") REFERENCES "orgunit"."organizational_units" ("id") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
-- Modify "resource" table
ALTER TABLE "webhook"."resource" ADD CONSTRAINT "fk_resource_organizational_unit" FOREIGN KEY ("organizational_unit") REFERENCES "orgunit"."organizational_units" ("name") ON UPDATE NO ACTION ON DELETE NO ACTION NOT VALID;
