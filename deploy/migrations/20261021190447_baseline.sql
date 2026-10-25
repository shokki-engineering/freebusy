-- Copyright 2026 Srikanth K.
-- SPDX-License-Identifier: AGPL-3.0-or-later

-- Add new schema named "allocation"
CREATE SCHEMA "allocation";
-- Add new schema named "availability"
CREATE SCHEMA "availability";
-- Add new schema named "calendar"
CREATE SCHEMA "calendar";
-- Add new schema named "card"
CREATE SCHEMA "card";
-- Add new schema named "common"
CREATE SCHEMA "common";
-- Add new schema named "event"
CREATE SCHEMA "event";
-- Add new schema named "journal"
CREATE SCHEMA "journal";
-- Add new schema named "ldapgroup"
CREATE SCHEMA "ldapgroup";
-- Add new schema named "membership"
CREATE SCHEMA "membership";
-- Add new schema named "organization"
CREATE SCHEMA "organization";
-- Add new schema named "orgunit"
CREATE SCHEMA "orgunit";
-- Add new schema named "pack"
CREATE SCHEMA "pack";
-- Add new schema named "project"
CREATE SCHEMA "project";
-- Add new schema named "reservation"
CREATE SCHEMA "reservation";
-- Add new schema named "resource"
CREATE SCHEMA "resource";
-- Add new schema named "scimgroup"
CREATE SCHEMA "scimgroup";
-- Add new schema named "sensor"
CREATE SCHEMA "sensor";
-- Add new schema named "shared"
CREATE SCHEMA "shared";
-- Add new schema named "stream"
CREATE SCHEMA "stream";
-- Add new schema named "todo"
CREATE SCHEMA "todo";
-- Add new schema named "unit"
CREATE SCHEMA "unit";
-- Add new schema named "user"
CREATE SCHEMA "user";
-- Add new schema named "vcard"
CREATE SCHEMA "vcard";
-- Add new schema named "webhook"
CREATE SCHEMA "webhook";
-- Create "holds" table
CREATE TABLE "allocation"."holds" ("id" text NOT NULL, "name" text NOT NULL, "window" text NOT NULL, "quantity" integer NULL, "expire_time" timestamptz NULL, "state" text NULL DEFAULT 'ACTIVE', "idempotency_key" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "organizational_unit_id" text NOT NULL, "resources" text[] NULL, "candidate_resources" text[] NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_holds_state" CHECK (state = ANY (ARRAY['ACTIVE'::text, 'RELEASED'::text, 'EXPIRED'::text, 'CONVERTED'::text])));
-- Create index "idx_allocation_holds_name" to table: "holds"
CREATE UNIQUE INDEX "idx_allocation_holds_name" ON "allocation"."holds" ("name");
-- Create index "idx_holds_candidate_resources_gin" to table: "holds"
CREATE INDEX "idx_holds_candidate_resources_gin" ON "allocation"."holds" USING gin ("candidate_resources");
-- Create index "idx_holds_idempotency_key_trgm" to table: "holds"
CREATE INDEX "idx_holds_idempotency_key_trgm" ON "allocation"."holds" USING gin ("idempotency_key" gin_trgm_ops);
-- Create index "idx_holds_organizational_unit_id_id" to table: "holds"
CREATE INDEX "idx_holds_organizational_unit_id_id" ON "allocation"."holds" ("organizational_unit_id", "id");
-- Create index "idx_holds_resources_gin" to table: "holds"
CREATE INDEX "idx_holds_resources_gin" ON "allocation"."holds" USING gin ("resources");
-- Create "resource" table
CREATE TABLE "allocation"."resource" ("id" text NOT NULL, "name" text NOT NULL, "resource" text NOT NULL, "start_time" timestamptz NOT NULL, "end_time" timestamptz NOT NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "hold" text NULL, "organizational_unit_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "resource_resource_no_overlap" EXCLUDE USING gist ("resource" WITH =, (tstzrange(start_time, end_time, '[)'::text)) WITH &&));
-- Create index "idx_allocation_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_allocation_resource_name" ON "allocation"."resource" ("name");
-- Create index "idx_resource_hold" to table: "resource"
CREATE INDEX "idx_resource_hold" ON "allocation"."resource" ("hold");
-- Create index "idx_resource_organizational_unit_id_id" to table: "resource"
CREATE INDEX "idx_resource_organizational_unit_id_id" ON "allocation"."resource" ("organizational_unit_id", "id");
-- Create index "idx_resource_resource" to table: "resource"
CREATE INDEX "idx_resource_resource" ON "allocation"."resource" ("resource");
-- Create index "idx_resource_resource_start_time_end_time" to table: "resource"
CREATE INDEX "idx_resource_resource_start_time_end_time" ON "allocation"."resource" ("resource", "start_time", "end_time");
-- Create "availabilities" table
CREATE TABLE "availability"."availabilities" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "ical_uid" text NULL, "busy_type" text NULL, "duration" text NULL, "summary" text NULL, "description" text NULL, "organizer" text NULL, "priority" integer NULL, "categories" text[] NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "end_form_case" text NULL, "calendar_id" text NOT NULL, "start_id" text NULL, "end_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_availabilities_busy_type" CHECK (busy_type = ANY (ARRAY['BUSY'::text, 'BUSY_UNAVAILABLE'::text, 'BUSY_TENTATIVE'::text])), CONSTRAINT "chk_availabilities_end_form_case" CHECK (end_form_case = ANY (ARRAY['END'::text, 'DURATION'::text])));
-- Create index "idx_availabilities_calendar_id_id" to table: "availabilities"
CREATE INDEX "idx_availabilities_calendar_id_id" ON "availability"."availabilities" ("calendar_id", "id");
-- Create index "idx_availabilities_categories_gin" to table: "availabilities"
CREATE INDEX "idx_availabilities_categories_gin" ON "availability"."availabilities" USING gin ("categories");
-- Create index "idx_availabilities_delete_time" to table: "availabilities"
CREATE INDEX "idx_availabilities_delete_time" ON "availability"."availabilities" ("delete_time");
-- Create index "idx_availabilities_end_id" to table: "availabilities"
CREATE INDEX "idx_availabilities_end_id" ON "availability"."availabilities" ("end_id");
-- Create index "idx_availabilities_start_id" to table: "availabilities"
CREATE INDEX "idx_availabilities_start_id" ON "availability"."availabilities" ("start_id");
-- Create index "idx_availability_availabilities_name" to table: "availabilities"
CREATE UNIQUE INDEX "idx_availability_availabilities_name" ON "availability"."availabilities" ("name");
-- Create index "idx_availability_availabilities_uid" to table: "availabilities"
CREATE UNIQUE INDEX "idx_availability_availabilities_uid" ON "availability"."availabilities" ("uid");
-- Create "available_periods" table
CREATE TABLE "availability"."available_periods" ("id" text NOT NULL, "ical_uid" text NULL, "duration" text NULL, "summary" text NULL, "description" text NULL, "location" text NULL, "recurrence_rule" text NULL, "categories" text[] NULL, "comments" text[] NULL, "end_form_case" text NULL, "availability_id" text NOT NULL, "start_id" text NOT NULL, "end_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_available_periods_end_form_case" CHECK (end_form_case = ANY (ARRAY['END'::text, 'DURATION'::text])));
-- Create index "idx_available_periods_availability_id" to table: "available_periods"
CREATE INDEX "idx_available_periods_availability_id" ON "availability"."available_periods" ("availability_id");
-- Create index "idx_available_periods_categories_gin" to table: "available_periods"
CREATE INDEX "idx_available_periods_categories_gin" ON "availability"."available_periods" USING gin ("categories");
-- Create index "idx_available_periods_comments_gin" to table: "available_periods"
CREATE INDEX "idx_available_periods_comments_gin" ON "availability"."available_periods" USING gin ("comments");
-- Create index "idx_available_periods_end_id" to table: "available_periods"
CREATE INDEX "idx_available_periods_end_id" ON "availability"."available_periods" ("end_id");
-- Create index "idx_available_periods_start_id" to table: "available_periods"
CREATE INDEX "idx_available_periods_start_id" ON "availability"."available_periods" ("start_id");
-- Create "calendar_times" table
CREATE TABLE "availability"."calendar_times" ("id" text NOT NULL, "date" date NULL, "date_time" timestamptz NULL, "date_time_zone" text NULL, "value_case" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_calendar_times_value_case" CHECK (value_case = ANY (ARRAY['DATE'::text, 'DATE_TIME'::text])));
-- Create "extension_properties" table
CREATE TABLE "availability"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "availability_id" text NOT NULL, "available_period_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_availability_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_availability_id" ON "availability"."extension_properties" ("availability_id");
-- Create index "idx_extension_properties_available_period_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_available_period_id" ON "availability"."extension_properties" ("available_period_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "availability"."extension_properties" USING gin ("values");
-- Create "extension_properties" table
CREATE TABLE "calendar"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "calendar_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_calendar_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_calendar_id" ON "calendar"."extension_properties" ("calendar_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "calendar"."extension_properties" USING gin ("values");
-- Create "resource" table
CREATE TABLE "calendar"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "display_name" text NOT NULL, "description" text NULL, "scale" text NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "max_resource_size" bigint NULL, "min_date_time" timestamptz NULL, "max_date_time" timestamptz NULL, "max_instances" integer NULL, "max_attendees_per_instance" integer NULL, "time_zone_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_scale" CHECK (scale = 'GREGORIAN'::text));
-- Create index "idx_calendar_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_calendar_resource_name" ON "calendar"."resource" ("name");
-- Create index "idx_calendar_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_calendar_resource_uid" ON "calendar"."resource" ("uid");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "calendar"."resource" ("delete_time");
-- Create index "idx_resource_time_zone_id" to table: "resource"
CREATE INDEX "idx_resource_time_zone_id" ON "calendar"."resource" ("time_zone_id");
-- Create "name_components" table
CREATE TABLE "card"."name_components" ("id" text NOT NULL, "value" text NOT NULL, "kind" text NOT NULL DEFAULT 'TITLE', "phonetic" text NULL, "name_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_name_components_kind" CHECK (kind = ANY (ARRAY['TITLE'::text, 'GIVEN'::text, 'GIVEN2'::text, 'SURNAME'::text, 'SURNAME2'::text, 'CREDENTIAL'::text, 'GENERATION'::text, 'SEPARATOR'::text])));
-- Create index "idx_name_components_name_id" to table: "name_components"
CREATE INDEX "idx_name_components_name_id" ON "card"."name_components" ("name_id");
-- Create "names" table
CREATE TABLE "card"."names" ("id" text NOT NULL, "is_ordered" boolean NULL, "default_separator" text NULL, "display_name" text NULL, "sort_as" jsonb NULL, "phonetic_script" text NULL, "phonetic_system" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_names_phonetic_system" CHECK (phonetic_system = ANY (ARRAY['IPA'::text, 'PINY'::text, 'JYUT'::text])));
-- Create "resource" table
CREATE TABLE "card"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "jscontact_uid" text NULL, "kind" text NULL, "nicknames" jsonb NULL, "organizations" jsonb NULL, "titles" jsonb NULL, "emails" jsonb NULL, "online_services" jsonb NULL, "phones" jsonb NULL, "preferred_languages" jsonb NULL, "calendars" jsonb NULL, "scheduling_addresses" jsonb NULL, "addresses" jsonb NULL, "crypto_keys" jsonb NULL, "directories" jsonb NULL, "links" jsonb NULL, "media" jsonb NULL, "anniversaries" jsonb NULL, "keywords" text[] NULL, "notes" jsonb NULL, "personal_info" jsonb NULL, "relations" jsonb NULL, "members" text[] NULL, "language_code" text NULL, "product_id" text NULL, "jscontact_create_time" timestamptz NULL, "jscontact_update_time" timestamptz NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "jscontact_props" jsonb NULL, "name_components_id" text NULL, "speech_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_kind" CHECK (kind = ANY (ARRAY['INDIVIDUAL'::text, 'GROUP'::text, 'ORG'::text, 'LOCATION'::text, 'DEVICE'::text, 'APPLICATION'::text])));
-- Create index "idx_card_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_card_resource_name" ON "card"."resource" ("name");
-- Create index "idx_card_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_card_resource_uid" ON "card"."resource" ("uid");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "card"."resource" ("delete_time");
-- Create index "idx_resource_keywords_gin" to table: "resource"
CREATE INDEX "idx_resource_keywords_gin" ON "card"."resource" USING gin ("keywords");
-- Create index "idx_resource_members_gin" to table: "resource"
CREATE INDEX "idx_resource_members_gin" ON "card"."resource" USING gin ("members");
-- Create index "idx_resource_name_components_id" to table: "resource"
CREATE INDEX "idx_resource_name_components_id" ON "card"."resource" ("name_components_id");
-- Create index "idx_resource_speech_id" to table: "resource"
CREATE INDEX "idx_resource_speech_id" ON "card"."resource" ("speech_id");
-- Create "speeches" table
CREATE TABLE "card"."speeches" ("id" text NOT NULL, "grammatical_gender" text NULL, "pronouns" jsonb NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_speeches_grammatical_gender" CHECK (grammatical_gender = ANY (ARRAY['ANIMATE'::text, 'COMMON'::text, 'FEMININE'::text, 'INANIMATE'::text, 'MASCULINE'::text, 'NEUTER'::text])));
-- Create "fractions" table
CREATE TABLE "common"."fractions" ("id" text NOT NULL, "numerator" bigint NULL, "denominator" bigint NULL, PRIMARY KEY ("id"));
-- Create "time_zones" table
CREATE TABLE "common"."time_zones" ("id" text NOT NULL, "version" text NULL, PRIMARY KEY ("id"));
-- Create "alarms" table
CREATE TABLE "event"."alarms" ("id" text NOT NULL, "action" text NOT NULL DEFAULT 'AUDIO', "trigger_offset" text NULL, "trigger_time" timestamptz NULL, "trigger_relation" text NULL, "repeat_interval" text NULL, "repeat_count" integer NULL, "description" text NULL, "summary" text NULL, "ical_uid" text NULL, "acknowledged_time" timestamptz NULL, "proximity" text NULL, "snoozed_alarm_uid" text NULL, "trigger_case" text NULL, "event_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_alarms_action" CHECK (action = ANY (ARRAY['AUDIO'::text, 'DISPLAY'::text, 'EMAIL'::text])), CONSTRAINT "chk_alarms_proximity" CHECK (proximity = ANY (ARRAY['ARRIVE'::text, 'DEPART'::text, 'CONNECT'::text, 'DISCONNECT'::text])), CONSTRAINT "chk_alarms_trigger_case" CHECK (trigger_case = ANY (ARRAY['TRIGGER_OFFSET'::text, 'TRIGGER_TIME'::text])), CONSTRAINT "chk_alarms_trigger_relation" CHECK (trigger_relation = ANY (ARRAY['START'::text, 'END'::text])));
-- Create index "idx_alarms_event_id" to table: "alarms"
CREATE INDEX "idx_alarms_event_id" ON "event"."alarms" ("event_id");
-- Create "attendees" table
CREATE TABLE "event"."attendees" ("id" text NOT NULL, "address" text NOT NULL, "display_name" text NULL, "role" text NULL, "participation" text NULL, "user_type" text NULL, "rsvp" boolean NULL, "delegatees" text[] NULL, "delegators" text[] NULL, "event_id" text NOT NULL, "alarm_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_attendees_participation" CHECK (participation = ANY (ARRAY['NEEDS_ACTION'::text, 'ACCEPTED'::text, 'DECLINED'::text, 'TENTATIVE'::text, 'DELEGATED'::text])), CONSTRAINT "chk_attendees_role" CHECK (role = ANY (ARRAY['CHAIR'::text, 'REQUIRED'::text, 'OPTIONAL'::text, 'NON_PARTICIPANT'::text])), CONSTRAINT "chk_attendees_user_type" CHECK (user_type = ANY (ARRAY['INDIVIDUAL'::text, 'GROUP'::text, 'RESOURCE'::text, 'ROOM'::text, 'UNKNOWN'::text])));
-- Create index "idx_attendees_alarm_id" to table: "attendees"
CREATE INDEX "idx_attendees_alarm_id" ON "event"."attendees" ("alarm_id");
-- Create index "idx_attendees_delegatees_gin" to table: "attendees"
CREATE INDEX "idx_attendees_delegatees_gin" ON "event"."attendees" USING gin ("delegatees");
-- Create index "idx_attendees_delegators_gin" to table: "attendees"
CREATE INDEX "idx_attendees_delegators_gin" ON "event"."attendees" USING gin ("delegators");
-- Create index "idx_attendees_event_id" to table: "attendees"
CREATE INDEX "idx_attendees_event_id" ON "event"."attendees" ("event_id");
-- Create "calendar_times" table
CREATE TABLE "event"."calendar_times" ("id" text NOT NULL, "date" date NULL, "date_time" timestamptz NULL, "date_time_zone" text NULL, "value_case" text NULL, "event_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_calendar_times_value_case" CHECK (value_case = ANY (ARRAY['DATE'::text, 'DATE_TIME'::text])));
-- Create index "idx_calendar_times_event_id" to table: "calendar_times"
CREATE INDEX "idx_calendar_times_event_id" ON "event"."calendar_times" ("event_id");
-- Create "extension_properties" table
CREATE TABLE "event"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "event_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_event_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_event_id" ON "event"."extension_properties" ("event_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "event"."extension_properties" USING gin ("values");
-- Create "links" table
CREATE TABLE "event"."links" ("id" text NOT NULL, "uri" text NULL, "ical_uid" text NULL, "xml_reference" text NULL, "relation" text NOT NULL, "format_type" text NULL, "label" text NULL, "language_code" text NULL, "value_case" text NULL, "event_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_links_value_case" CHECK (value_case = ANY (ARRAY['URI'::text, 'ICAL_UID'::text, 'XML_REFERENCE'::text])));
-- Create index "idx_links_event_id" to table: "links"
CREATE INDEX "idx_links_event_id" ON "event"."links" ("event_id");
-- Create "locations" table
CREATE TABLE "event"."locations" ("id" text NOT NULL, "ical_uid" text NULL, "display_name" text NULL, "description" text NULL, "location_types" text[] NULL, "position" text NULL, "event_id" text NOT NULL, "participant_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_locations_event_id" to table: "locations"
CREATE INDEX "idx_locations_event_id" ON "event"."locations" ("event_id");
-- Create index "idx_locations_location_types_gin" to table: "locations"
CREATE INDEX "idx_locations_location_types_gin" ON "event"."locations" USING gin ("location_types");
-- Create index "idx_locations_participant_id" to table: "locations"
CREATE INDEX "idx_locations_participant_id" ON "event"."locations" ("participant_id");
-- Create "organizers" table
CREATE TABLE "event"."organizers" ("id" text NOT NULL, "address" text NOT NULL, "display_name" text NULL, "sender" text NULL, PRIMARY KEY ("id"));
-- Create "participants" table
CREATE TABLE "event"."participants" ("id" text NOT NULL, "ical_uid" text NULL, "participant_types" text NOT NULL, "calendar_address" text NULL, "summary" text NULL, "description" text NULL, "url" text NULL, "position" text NULL, "event_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_participants_event_id" to table: "participants"
CREATE INDEX "idx_participants_event_id" ON "event"."participants" ("event_id");
-- Create "recurrences" table
CREATE TABLE "event"."recurrences" ("id" text NOT NULL, "frequency" text NOT NULL DEFAULT 'SECONDLY', "count" integer NULL, "interval" integer NULL, "second_numbers" integer[] NULL, "minute_numbers" integer[] NULL, "hour_numbers" integer[] NULL, "month_days" integer[] NULL, "year_days" integer[] NULL, "week_numbers" integer[] NULL, "months" text NULL, "set_positions" integer[] NULL, "week_start" text NULL, "rscale" text NULL, "skip" text NULL, "leap_months" text NULL, "bound_case" text NULL, "until_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_recurrences_bound_case" CHECK (bound_case = ANY (ARRAY['UNTIL'::text, 'COUNT'::text])), CONSTRAINT "chk_recurrences_frequency" CHECK (frequency = ANY (ARRAY['SECONDLY'::text, 'MINUTELY'::text, 'HOURLY'::text, 'DAILY'::text, 'WEEKLY'::text, 'MONTHLY'::text, 'YEARLY'::text])), CONSTRAINT "chk_recurrences_skip" CHECK (skip = ANY (ARRAY['OMIT'::text, 'BACKWARD'::text, 'FORWARD'::text])), CONSTRAINT "chk_recurrences_week_start" CHECK (week_start = ANY (ARRAY['MONDAY'::text, 'TUESDAY'::text, 'WEDNESDAY'::text, 'THURSDAY'::text, 'FRIDAY'::text, 'SATURDAY'::text, 'SUNDAY'::text])));
-- Create index "idx_recurrences_until_id" to table: "recurrences"
CREATE INDEX "idx_recurrences_until_id" ON "event"."recurrences" ("until_id");
-- Create "resource" table
CREATE TABLE "event"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "ical_uid" text NULL, "summary" text NULL, "description" text NULL, "location" text NULL, "duration" text NULL, "confirmation" text NULL, "classification" text NULL, "transparency" text NULL, "sequence" integer NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "position" text NULL, "concepts" text[] NULL, "refids" text[] NULL, "ends_case" text NULL, "calendar_id" text NOT NULL, "start_id" text NOT NULL, "end_id" text NULL, "recurrence_id" text NULL, "original_start_id" text NULL, "organizer_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_classification" CHECK (classification = ANY (ARRAY['PUBLIC'::text, 'PRIVATE'::text, 'CONFIDENTIAL'::text])), CONSTRAINT "chk_resource_confirmation" CHECK (confirmation = ANY (ARRAY['TENTATIVE'::text, 'CONFIRMED'::text, 'CANCELLED'::text])), CONSTRAINT "chk_resource_ends_case" CHECK (ends_case = ANY (ARRAY['END'::text, 'DURATION'::text])), CONSTRAINT "chk_resource_transparency" CHECK (transparency = ANY (ARRAY['OPAQUE'::text, 'TRANSPARENT'::text])));
-- Create index "idx_event_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_event_resource_name" ON "event"."resource" ("name");
-- Create index "idx_event_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_event_resource_uid" ON "event"."resource" ("uid");
-- Create index "idx_resource_calendar_id_id" to table: "resource"
CREATE INDEX "idx_resource_calendar_id_id" ON "event"."resource" ("calendar_id", "id");
-- Create index "idx_resource_concepts_gin" to table: "resource"
CREATE INDEX "idx_resource_concepts_gin" ON "event"."resource" USING gin ("concepts");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "event"."resource" ("delete_time");
-- Create index "idx_resource_end_id" to table: "resource"
CREATE INDEX "idx_resource_end_id" ON "event"."resource" ("end_id");
-- Create index "idx_resource_organizer_id" to table: "resource"
CREATE INDEX "idx_resource_organizer_id" ON "event"."resource" ("organizer_id");
-- Create index "idx_resource_original_start_id" to table: "resource"
CREATE INDEX "idx_resource_original_start_id" ON "event"."resource" ("original_start_id");
-- Create index "idx_resource_recurrence_id" to table: "resource"
CREATE INDEX "idx_resource_recurrence_id" ON "event"."resource" ("recurrence_id");
-- Create index "idx_resource_refids_gin" to table: "resource"
CREATE INDEX "idx_resource_refids_gin" ON "event"."resource" USING gin ("refids");
-- Create index "idx_resource_start_id" to table: "resource"
CREATE INDEX "idx_resource_start_id" ON "event"."resource" ("start_id");
-- Create "resources" table
CREATE TABLE "event"."resources" ("id" text NOT NULL, "ical_uid" text NULL, "display_name" text NULL, "description" text NULL, "resource_types" text[] NULL, "position" text NULL, "event_id" text NOT NULL, "participant_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_resources_event_id" to table: "resources"
CREATE INDEX "idx_resources_event_id" ON "event"."resources" ("event_id");
-- Create index "idx_resources_participant_id" to table: "resources"
CREATE INDEX "idx_resources_participant_id" ON "event"."resources" ("participant_id");
-- Create index "idx_resources_resource_types_gin" to table: "resources"
CREATE INDEX "idx_resources_resource_types_gin" ON "event"."resources" USING gin ("resource_types");
-- Create "structured_datas" table
CREATE TABLE "event"."structured_datas" ("id" text NOT NULL, "text" text NULL, "uri" text NULL, "binary" bytea NULL, "format_type" text NULL, "schema" text NULL, "value_case" text NULL, "event_id" text NOT NULL, "participant_id" text NOT NULL, "location_id" text NOT NULL, "resource_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_structured_datas_value_case" CHECK (value_case = ANY (ARRAY['TEXT'::text, 'URI'::text, 'BINARY'::text])));
-- Create index "idx_structured_datas_event_id" to table: "structured_datas"
CREATE INDEX "idx_structured_datas_event_id" ON "event"."structured_datas" ("event_id");
-- Create index "idx_structured_datas_location_id" to table: "structured_datas"
CREATE INDEX "idx_structured_datas_location_id" ON "event"."structured_datas" ("location_id");
-- Create index "idx_structured_datas_participant_id" to table: "structured_datas"
CREATE INDEX "idx_structured_datas_participant_id" ON "event"."structured_datas" ("participant_id");
-- Create index "idx_structured_datas_resource_id" to table: "structured_datas"
CREATE INDEX "idx_structured_datas_resource_id" ON "event"."structured_datas" ("resource_id");
-- Create "styled_descriptions" table
CREATE TABLE "event"."styled_descriptions" ("id" text NOT NULL, "text" text NULL, "uri" text NULL, "format_type" text NULL, "derived" boolean NULL, "value_case" text NULL, "event_id" text NOT NULL, "participant_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_styled_descriptions_value_case" CHECK (value_case = ANY (ARRAY['TEXT'::text, 'URI'::text])));
-- Create index "idx_styled_descriptions_event_id" to table: "styled_descriptions"
CREATE INDEX "idx_styled_descriptions_event_id" ON "event"."styled_descriptions" ("event_id");
-- Create index "idx_styled_descriptions_participant_id" to table: "styled_descriptions"
CREATE INDEX "idx_styled_descriptions_participant_id" ON "event"."styled_descriptions" ("participant_id");
-- Create "weekday_nums" table
CREATE TABLE "event"."weekday_nums" ("id" text NOT NULL, "day" text NOT NULL DEFAULT 'MONDAY', "ordinal" integer NULL, "recurrence_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_weekday_nums_day" CHECK (day = ANY (ARRAY['MONDAY'::text, 'TUESDAY'::text, 'WEDNESDAY'::text, 'THURSDAY'::text, 'FRIDAY'::text, 'SATURDAY'::text, 'SUNDAY'::text])));
-- Create index "idx_weekday_nums_recurrence_id" to table: "weekday_nums"
CREATE INDEX "idx_weekday_nums_recurrence_id" ON "event"."weekday_nums" ("recurrence_id");
-- Create "calendar_times" table
CREATE TABLE "journal"."calendar_times" ("id" text NOT NULL, "date" date NULL, "date_time" timestamptz NULL, "date_time_zone" text NULL, "value_case" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_calendar_times_value_case" CHECK (value_case = ANY (ARRAY['DATE'::text, 'DATE_TIME'::text])));
-- Create "extension_properties" table
CREATE TABLE "journal"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "journal_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_journal_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_journal_id" ON "journal"."extension_properties" ("journal_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "journal"."extension_properties" USING gin ("values");
-- Create "resource" table
CREATE TABLE "journal"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "ical_uid" text NULL, "summary" text NULL, "descriptions" text[] NULL, "publication" text NULL, "classification" text NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "calendar_id" text NOT NULL, "start_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_classification" CHECK (classification = ANY (ARRAY['PUBLIC'::text, 'PRIVATE'::text, 'CONFIDENTIAL'::text])), CONSTRAINT "chk_resource_publication" CHECK (publication = ANY (ARRAY['DRAFT'::text, 'FINAL'::text, 'CANCELLED'::text])));
-- Create index "idx_journal_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_journal_resource_name" ON "journal"."resource" ("name");
-- Create index "idx_journal_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_journal_resource_uid" ON "journal"."resource" ("uid");
-- Create index "idx_resource_calendar_id_id" to table: "resource"
CREATE INDEX "idx_resource_calendar_id_id" ON "journal"."resource" ("calendar_id", "id");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "journal"."resource" ("delete_time");
-- Create index "idx_resource_descriptions_gin" to table: "resource"
CREATE INDEX "idx_resource_descriptions_gin" ON "journal"."resource" USING gin ("descriptions");
-- Create index "idx_resource_start_id" to table: "resource"
CREATE INDEX "idx_resource_start_id" ON "journal"."resource" ("start_id");
-- Create "resource" table
CREATE TABLE "ldapgroup"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "display_names" text[] NOT NULL, "descriptions" text[] NULL, "owners" text[] NULL, "organization" text NULL, "business_categories" text[] NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_ldapgroup_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_ldapgroup_resource_name" ON "ldapgroup"."resource" ("name");
-- Create index "idx_ldapgroup_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_ldapgroup_resource_uid" ON "ldapgroup"."resource" ("uid");
-- Create index "idx_resource_business_categories_gin" to table: "resource"
CREATE INDEX "idx_resource_business_categories_gin" ON "ldapgroup"."resource" USING gin ("business_categories");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "ldapgroup"."resource" ("delete_time");
-- Create index "idx_resource_descriptions_gin" to table: "resource"
CREATE INDEX "idx_resource_descriptions_gin" ON "ldapgroup"."resource" USING gin ("descriptions");
-- Create index "idx_resource_display_names_gin" to table: "resource"
CREATE INDEX "idx_resource_display_names_gin" ON "ldapgroup"."resource" USING gin ("display_names");
-- Create index "idx_resource_organization" to table: "resource"
CREATE INDEX "idx_resource_organization" ON "ldapgroup"."resource" ("organization");
-- Create index "idx_resource_owners_gin" to table: "resource"
CREATE INDEX "idx_resource_owners_gin" ON "ldapgroup"."resource" USING gin ("owners");
-- Create "resource" table
CREATE TABLE "membership"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "member" text NOT NULL, "role" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "ldap_group_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_membership_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_membership_resource_name" ON "membership"."resource" ("name");
-- Create index "idx_membership_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_membership_resource_uid" ON "membership"."resource" ("uid");
-- Create index "idx_resource_ldap_group_id_id" to table: "resource"
CREATE INDEX "idx_resource_ldap_group_id_id" ON "membership"."resource" ("ldap_group_id", "id");
-- Create index "idx_resource_member" to table: "resource"
CREATE INDEX "idx_resource_member" ON "membership"."resource" ("member");
-- Create "resource" table
CREATE TABLE "organization"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "display_names" text[] NOT NULL, "descriptions" text[] NULL, "business_categories" text[] NULL, "see_also" text[] NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_organization_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_organization_resource_name" ON "organization"."resource" ("name");
-- Create index "idx_organization_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_organization_resource_uid" ON "organization"."resource" ("uid");
-- Create index "idx_resource_business_categories_gin" to table: "resource"
CREATE INDEX "idx_resource_business_categories_gin" ON "organization"."resource" USING gin ("business_categories");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "organization"."resource" ("delete_time");
-- Create index "idx_resource_descriptions_gin" to table: "resource"
CREATE INDEX "idx_resource_descriptions_gin" ON "organization"."resource" USING gin ("descriptions");
-- Create index "idx_resource_display_names_gin" to table: "resource"
CREATE INDEX "idx_resource_display_names_gin" ON "organization"."resource" USING gin ("display_names");
-- Create index "idx_resource_see_also_gin" to table: "resource"
CREATE INDEX "idx_resource_see_also_gin" ON "organization"."resource" USING gin ("see_also");
-- Create "organizational_units" table
CREATE TABLE "orgunit"."organizational_units" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "display_names" text[] NOT NULL, "descriptions" text[] NULL, "business_categories" text[] NULL, "see_also" text[] NULL, "organization" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_organizational_units_business_categories_gin" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_business_categories_gin" ON "orgunit"."organizational_units" USING gin ("business_categories");
-- Create index "idx_organizational_units_delete_time" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_delete_time" ON "orgunit"."organizational_units" ("delete_time");
-- Create index "idx_organizational_units_descriptions_gin" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_descriptions_gin" ON "orgunit"."organizational_units" USING gin ("descriptions");
-- Create index "idx_organizational_units_display_names_gin" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_display_names_gin" ON "orgunit"."organizational_units" USING gin ("display_names");
-- Create index "idx_organizational_units_organization" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_organization" ON "orgunit"."organizational_units" ("organization");
-- Create index "idx_organizational_units_see_also_gin" to table: "organizational_units"
CREATE INDEX "idx_organizational_units_see_also_gin" ON "orgunit"."organizational_units" USING gin ("see_also");
-- Create index "idx_orgunit_organizational_units_name" to table: "organizational_units"
CREATE UNIQUE INDEX "idx_orgunit_organizational_units_name" ON "orgunit"."organizational_units" ("name");
-- Create index "idx_orgunit_organizational_units_uid" to table: "organizational_units"
CREATE UNIQUE INDEX "idx_orgunit_organizational_units_uid" ON "orgunit"."organizational_units" ("uid");
-- Create "extension_fields" table
CREATE TABLE "pack"."extension_fields" ("id" text NOT NULL, "label" text NOT NULL, "string_value" text NULL, "number_value" double precision NULL, "boolean_value" boolean NULL, "data_value" bytea NULL, "value_case" text NULL, "record_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_extension_fields_value_case" CHECK (value_case = ANY (ARRAY['STRING_VALUE'::text, 'NUMBER_VALUE'::text, 'BOOLEAN_VALUE'::text, 'DATA_VALUE'::text])));
-- Create index "idx_extension_fields_record_id" to table: "extension_fields"
CREATE INDEX "idx_extension_fields_record_id" ON "pack"."extension_fields" ("record_id");
-- Create "records" table
CREATE TABLE "pack"."records" ("id" text NOT NULL, "base_sensor_id" text NULL, "base_time_value" double precision NULL, "base_unit" text NULL, "base_value" double precision NULL, "base_sum" double precision NULL, "sensor_id" text NULL, "unit" text NULL, "sum" double precision NULL, "time_value" double precision NULL, "update_duration" text NULL, "number_value" double precision NULL, "string_value" text NULL, "boolean_value" boolean NULL, "data_value" bytea NULL, "value_case" text NULL, "pack_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_records_value_case" CHECK (value_case = ANY (ARRAY['NUMBER_VALUE'::text, 'STRING_VALUE'::text, 'BOOLEAN_VALUE'::text, 'DATA_VALUE'::text])));
-- Create index "idx_records_pack_id" to table: "records"
CREATE INDEX "idx_records_pack_id" ON "pack"."records" ("pack_id");
-- Create "resource" table
CREATE TABLE "pack"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "base_version" integer NULL, "resolved" boolean NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_pack_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_pack_resource_name" ON "pack"."resource" ("name");
-- Create index "idx_pack_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_pack_resource_uid" ON "pack"."resource" ("uid");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "pack"."resource" ("delete_time");
-- Create "resource" table
CREATE TABLE "project"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "display_name" text NOT NULL, "description" text NULL, "owner" text NULL, "start_time" timestamptz NULL, "end_time" timestamptz NULL, "state" text NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_state" CHECK (state = ANY (ARRAY['ACTIVE'::text, 'PAUSED'::text, 'COMPLETED'::text, 'CANCELLED'::text])));
-- Create index "idx_project_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_project_resource_name" ON "project"."resource" ("name");
-- Create index "idx_project_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_project_resource_uid" ON "project"."resource" ("uid");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "project"."resource" ("delete_time");
-- Create index "idx_resource_owner" to table: "resource"
CREATE INDEX "idx_resource_owner" ON "project"."resource" ("owner");
-- Create "resource" table
CREATE TABLE "reservation"."resource" ("id" text NOT NULL, "name" text NOT NULL, "hold" text NOT NULL, "start_time" timestamptz NULL, "end_time" timestamptz NULL, "customer" text NULL, "state" text NULL DEFAULT 'PENDING', "attributes" jsonb NULL, "cancel_reason" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "confirm_time" timestamptz NULL, "cancel_time" timestamptz NULL, "etag" text NULL, "replaces" text NULL, "replacement" text NULL, "organizational_unit_id" text NOT NULL, "resources" text[] NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_state" CHECK (state = ANY (ARRAY['PENDING'::text, 'CONFIRMED'::text, 'CANCELLING'::text, 'CANCELLED'::text, 'FAILED'::text, 'RESCHEDULING'::text, 'RESCHEDULED'::text])));
-- Create index "idx_reservation_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_reservation_resource_name" ON "reservation"."resource" ("name");
-- Create index "idx_resource_customer_trgm" to table: "resource"
CREATE INDEX "idx_resource_customer_trgm" ON "reservation"."resource" USING gin ("customer" gin_trgm_ops);
-- Create index "idx_resource_hold" to table: "resource"
CREATE UNIQUE INDEX "idx_resource_hold" ON "reservation"."resource" ("hold");
-- Create index "idx_resource_organizational_unit_id_id" to table: "resource"
CREATE INDEX "idx_resource_organizational_unit_id_id" ON "reservation"."resource" ("organizational_unit_id", "id");
-- Create index "idx_resource_resources_gin" to table: "resource"
CREATE INDEX "idx_resource_resources_gin" ON "reservation"."resource" USING gin ("resources");
-- Create "entity" table
CREATE TABLE "resource"."entity" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "ical_uid" text NULL, "display_name" text NULL, "description" text NULL, "resource_types" text[] NULL, "position" text NULL, "calendar" text NULL, "organizational_unit" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_entity_calendar" to table: "entity"
CREATE INDEX "idx_entity_calendar" ON "resource"."entity" ("calendar");
-- Create index "idx_entity_delete_time" to table: "entity"
CREATE INDEX "idx_entity_delete_time" ON "resource"."entity" ("delete_time");
-- Create index "idx_entity_organizational_unit" to table: "entity"
CREATE INDEX "idx_entity_organizational_unit" ON "resource"."entity" ("organizational_unit");
-- Create index "idx_entity_resource_types_gin" to table: "entity"
CREATE INDEX "idx_entity_resource_types_gin" ON "resource"."entity" USING gin ("resource_types");
-- Create index "idx_resource_entity_name" to table: "entity"
CREATE UNIQUE INDEX "idx_resource_entity_name" ON "resource"."entity" ("name");
-- Create index "idx_resource_entity_uid" to table: "entity"
CREATE UNIQUE INDEX "idx_resource_entity_uid" ON "resource"."entity" ("uid");
-- Create "structured_datas" table
CREATE TABLE "resource"."structured_datas" ("id" text NOT NULL, "text" text NULL, "uri" text NULL, "binary" bytea NULL, "format_type" text NULL, "schema" text NULL, "value_case" text NULL, "resource_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_structured_datas_value_case" CHECK (value_case = ANY (ARRAY['TEXT'::text, 'URI'::text, 'BINARY'::text])));
-- Create index "idx_structured_datas_resource_id" to table: "structured_datas"
CREATE INDEX "idx_structured_datas_resource_id" ON "resource"."structured_datas" ("resource_id");
-- Create "groups" table
CREATE TABLE "scimgroup"."groups" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "external_uid" text NULL, "display_name" text NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_groups_delete_time" to table: "groups"
CREATE INDEX "idx_groups_delete_time" ON "scimgroup"."groups" ("delete_time");
-- Create index "idx_scimgroup_groups_name" to table: "groups"
CREATE UNIQUE INDEX "idx_scimgroup_groups_name" ON "scimgroup"."groups" ("name");
-- Create index "idx_scimgroup_groups_uid" to table: "groups"
CREATE UNIQUE INDEX "idx_scimgroup_groups_uid" ON "scimgroup"."groups" ("uid");
-- Create "members" table
CREATE TABLE "scimgroup"."members" ("id" text NOT NULL, "value" text NOT NULL, "reference_uri" text NULL, "attribute_type" text NULL, "display" text NULL, "group_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_members_group_id" to table: "members"
CREATE INDEX "idx_members_group_id" ON "scimgroup"."members" ("group_id");
-- Create "resource" table
CREATE TABLE "sensor"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "senml_id" text NOT NULL, "display_name" text NULL, "target_uri" text NULL, "resource_types" text[] NULL, "interface_descriptions" text[] NULL, "media_types" text[] NULL, "unit" text NULL, "update_duration" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "sensor"."resource" ("delete_time");
-- Create index "idx_resource_interface_descriptions_gin" to table: "resource"
CREATE INDEX "idx_resource_interface_descriptions_gin" ON "sensor"."resource" USING gin ("interface_descriptions");
-- Create index "idx_resource_media_types_gin" to table: "resource"
CREATE INDEX "idx_resource_media_types_gin" ON "sensor"."resource" USING gin ("media_types");
-- Create index "idx_resource_resource_types_gin" to table: "resource"
CREATE INDEX "idx_resource_resource_types_gin" ON "sensor"."resource" USING gin ("resource_types");
-- Create index "idx_sensor_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_sensor_resource_name" ON "sensor"."resource" ("name");
-- Create index "idx_sensor_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_sensor_resource_uid" ON "sensor"."resource" ("uid");
-- Create "idempotency_keys" table
CREATE TABLE "shared"."idempotency_keys" ("id" text NOT NULL, "name" text NOT NULL, "method" text NOT NULL, "request_id" text NOT NULL, "state" text NULL DEFAULT 'IN_FLIGHT', "response" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_idempotency_keys_state" CHECK (state = ANY (ARRAY['IN_FLIGHT'::text, 'DONE'::text])));
-- Create index "idx_idempotency_keys_method_request_id" to table: "idempotency_keys"
CREATE UNIQUE INDEX "idx_idempotency_keys_method_request_id" ON "shared"."idempotency_keys" ("method", "request_id");
-- Create index "idx_shared_idempotency_keys_name" to table: "idempotency_keys"
CREATE UNIQUE INDEX "idx_shared_idempotency_keys_name" ON "shared"."idempotency_keys" ("name");
-- Create "extension_fields" table
CREATE TABLE "stream"."extension_fields" ("id" text NOT NULL, "label" text NOT NULL, "string_value" text NULL, "number_value" double precision NULL, "boolean_value" boolean NULL, "data_value" bytea NULL, "value_case" text NULL, "record_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_extension_fields_value_case" CHECK (value_case = ANY (ARRAY['STRING_VALUE'::text, 'NUMBER_VALUE'::text, 'BOOLEAN_VALUE'::text, 'DATA_VALUE'::text])));
-- Create index "idx_extension_fields_record_id" to table: "extension_fields"
CREATE INDEX "idx_extension_fields_record_id" ON "stream"."extension_fields" ("record_id");
-- Create "records" table
CREATE TABLE "stream"."records" ("id" text NOT NULL, "base_sensor_id" text NULL, "base_time_value" double precision NULL, "base_unit" text NULL, "base_value" double precision NULL, "base_sum" double precision NULL, "sensor_id" text NULL, "unit" text NULL, "sum" double precision NULL, "time_value" double precision NULL, "update_duration" text NULL, "number_value" double precision NULL, "string_value" text NULL, "boolean_value" boolean NULL, "data_value" bytea NULL, "value_case" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_records_value_case" CHECK (value_case = ANY (ARRAY['NUMBER_VALUE'::text, 'STRING_VALUE'::text, 'BOOLEAN_VALUE'::text, 'DATA_VALUE'::text])));
-- Create "records_link" table
CREATE TABLE "stream"."records_link" ("id" text NOT NULL, "reference_time_value" double precision NULL, "stream_id" text NOT NULL, "record_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_records_link_record_id" to table: "records_link"
CREATE INDEX "idx_records_link_record_id" ON "stream"."records_link" ("record_id");
-- Create index "idx_records_link_stream_id" to table: "records_link"
CREATE INDEX "idx_records_link_stream_id" ON "stream"."records_link" ("stream_id");
-- Create "resource" table
CREATE TABLE "stream"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "base_version" integer NULL, "closed" boolean NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, PRIMARY KEY ("id"));
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "stream"."resource" ("delete_time");
-- Create index "idx_stream_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_stream_resource_name" ON "stream"."resource" ("name");
-- Create index "idx_stream_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_stream_resource_uid" ON "stream"."resource" ("uid");
-- Create "calendar_times" table
CREATE TABLE "todo"."calendar_times" ("id" text NOT NULL, "date" date NULL, "date_time" timestamptz NULL, "date_time_zone" text NULL, "value_case" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_calendar_times_value_case" CHECK (value_case = ANY (ARRAY['DATE'::text, 'DATE_TIME'::text])));
-- Create "extension_properties" table
CREATE TABLE "todo"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "todo_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_todo_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_todo_id" ON "todo"."extension_properties" ("todo_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "todo"."extension_properties" USING gin ("values");
-- Create "resource" table
CREATE TABLE "todo"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "ical_uid" text NULL, "summary" text NULL, "description" text NULL, "location" text NULL, "duration" text NULL, "percent_complete" integer NULL, "priority" integer NULL, "completion" text NULL, "classification" text NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "deadline_case" text NULL, "calendar_id" text NOT NULL, "start_id" text NULL, "due_id" text NULL, "completed_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_classification" CHECK (classification = ANY (ARRAY['PUBLIC'::text, 'PRIVATE'::text, 'CONFIDENTIAL'::text])), CONSTRAINT "chk_resource_completion" CHECK (completion = ANY (ARRAY['NEEDS_ACTION'::text, 'IN_PROCESS'::text, 'COMPLETED'::text, 'CANCELLED'::text])), CONSTRAINT "chk_resource_deadline_case" CHECK (deadline_case = ANY (ARRAY['DUE'::text, 'DURATION'::text])));
-- Create index "idx_resource_calendar_id_id" to table: "resource"
CREATE INDEX "idx_resource_calendar_id_id" ON "todo"."resource" ("calendar_id", "id");
-- Create index "idx_resource_completed_id" to table: "resource"
CREATE INDEX "idx_resource_completed_id" ON "todo"."resource" ("completed_id");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "todo"."resource" ("delete_time");
-- Create index "idx_resource_due_id" to table: "resource"
CREATE INDEX "idx_resource_due_id" ON "todo"."resource" ("due_id");
-- Create index "idx_resource_start_id" to table: "resource"
CREATE INDEX "idx_resource_start_id" ON "todo"."resource" ("start_id");
-- Create index "idx_todo_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_todo_resource_name" ON "todo"."resource" ("name");
-- Create index "idx_todo_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_todo_resource_uid" ON "todo"."resource" ("uid");
-- Create "resource" table
CREATE TABLE "unit"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "symbol" text NOT NULL, "description" text NULL, "senml_unit" text NULL, "reference" text NULL, "producing_discouraged" boolean NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "scale_id" text NULL, "offset_id" text NULL, PRIMARY KEY ("id"));
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "unit"."resource" ("delete_time");
-- Create index "idx_resource_offset_id" to table: "resource"
CREATE INDEX "idx_resource_offset_id" ON "unit"."resource" ("offset_id");
-- Create index "idx_resource_scale_id" to table: "resource"
CREATE INDEX "idx_resource_scale_id" ON "unit"."resource" ("scale_id");
-- Create index "idx_unit_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_unit_resource_name" ON "unit"."resource" ("name");
-- Create index "idx_unit_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_unit_resource_uid" ON "unit"."resource" ("uid");
-- Create "address" table
CREATE TABLE "user"."address" ("id" text NOT NULL, "full_address" text NULL, "street_address" text NULL, "locality" text NULL, "region" text NULL, "postal_code" text NULL, "region_code" text NULL, "attribute_type" text NULL, "primary" boolean NULL, "user_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_address_user_id" to table: "address"
CREATE INDEX "idx_address_user_id" ON "user"."address" ("user_id");
-- Create "attributes" table
CREATE TABLE "user"."attributes" ("id" text NOT NULL, "value" text NULL, "display" text NULL, "attribute_type" text NULL, "primary" boolean NULL, "reference_uri" text NULL, "user_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_attributes_user_id" to table: "attributes"
CREATE INDEX "idx_attributes_user_id" ON "user"."attributes" ("user_id");
-- Create "enterprise_users" table
CREATE TABLE "user"."enterprise_users" ("id" text NOT NULL, "employee_number" text NULL, "cost_center" text NULL, "organization" text NULL, "division" text NULL, "department" text NULL, "manager_id" text NULL, PRIMARY KEY ("id"));
-- Create index "idx_enterprise_users_manager_id" to table: "enterprise_users"
CREATE INDEX "idx_enterprise_users_manager_id" ON "user"."enterprise_users" ("manager_id");
-- Create "managers" table
CREATE TABLE "user"."managers" ("id" text NOT NULL, "value" text NULL, "reference_uri" text NULL, "display_name" text NULL, PRIMARY KEY ("id"));
-- Create "names" table
CREATE TABLE "user"."names" ("id" text NOT NULL, "display_name" text NULL, "family_name" text NULL, "given_name" text NULL, "middle_names" text NULL, "honorific_prefix" text NULL, "honorific_suffix" text NULL, PRIMARY KEY ("id"));
-- Create "resource" table
CREATE TABLE "user"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "external_uid" text NULL, "username" text NOT NULL, "display_name" text NULL, "nickname" text NULL, "profile_uri" text NULL, "title" text NULL, "user_type" text NULL, "preferred_language_code" text NULL, "locale" text NULL, "time_zone" text NULL, "active" boolean NULL, "password" text NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "name_components_id" text NULL, "enterprise_id" text NULL, PRIMARY KEY ("id"));
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "user"."resource" ("delete_time");
-- Create index "idx_resource_enterprise_id" to table: "resource"
CREATE INDEX "idx_resource_enterprise_id" ON "user"."resource" ("enterprise_id");
-- Create index "idx_resource_name_components_id" to table: "resource"
CREATE INDEX "idx_resource_name_components_id" ON "user"."resource" ("name_components_id");
-- Create index "idx_user_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_user_resource_name" ON "user"."resource" ("name");
-- Create index "idx_user_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_user_resource_uid" ON "user"."resource" ("uid");
-- Create "address" table
CREATE TABLE "vcard"."address" ("id" text NOT NULL, "post_office_box" text[] NULL, "extended_addresses" text[] NULL, "street_addresses" text[] NULL, "localities" text[] NULL, "regions" text[] NULL, "postal_codes" text[] NULL, "countries" text[] NULL, "types" text NULL, "pref" integer NULL, "rooms" text[] NULL, "apartments" text[] NULL, "floors" text[] NULL, "street_numbers" text[] NULL, "streets" text[] NULL, "buildings" text[] NULL, "blocks" text[] NULL, "subdistricts" text[] NULL, "districts" text[] NULL, "landmarks" text[] NULL, "directions" text[] NULL, "label" text NULL, "phonetic" text NULL, "script" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_address_phonetic" CHECK (phonetic = ANY (ARRAY['IPA'::text, 'PINY'::text, 'JYUT'::text, 'SCRIPT'::text])));
-- Create index "idx_address_apartments_gin" to table: "address"
CREATE INDEX "idx_address_apartments_gin" ON "vcard"."address" USING gin ("apartments");
-- Create index "idx_address_blocks_gin" to table: "address"
CREATE INDEX "idx_address_blocks_gin" ON "vcard"."address" USING gin ("blocks");
-- Create index "idx_address_buildings_gin" to table: "address"
CREATE INDEX "idx_address_buildings_gin" ON "vcard"."address" USING gin ("buildings");
-- Create index "idx_address_contact_id" to table: "address"
CREATE INDEX "idx_address_contact_id" ON "vcard"."address" ("contact_id");
-- Create index "idx_address_countries_gin" to table: "address"
CREATE INDEX "idx_address_countries_gin" ON "vcard"."address" USING gin ("countries");
-- Create index "idx_address_directions_gin" to table: "address"
CREATE INDEX "idx_address_directions_gin" ON "vcard"."address" USING gin ("directions");
-- Create index "idx_address_districts_gin" to table: "address"
CREATE INDEX "idx_address_districts_gin" ON "vcard"."address" USING gin ("districts");
-- Create index "idx_address_extended_addresses_gin" to table: "address"
CREATE INDEX "idx_address_extended_addresses_gin" ON "vcard"."address" USING gin ("extended_addresses");
-- Create index "idx_address_floors_gin" to table: "address"
CREATE INDEX "idx_address_floors_gin" ON "vcard"."address" USING gin ("floors");
-- Create index "idx_address_landmarks_gin" to table: "address"
CREATE INDEX "idx_address_landmarks_gin" ON "vcard"."address" USING gin ("landmarks");
-- Create index "idx_address_localities_gin" to table: "address"
CREATE INDEX "idx_address_localities_gin" ON "vcard"."address" USING gin ("localities");
-- Create index "idx_address_post_office_box_gin" to table: "address"
CREATE INDEX "idx_address_post_office_box_gin" ON "vcard"."address" USING gin ("post_office_box");
-- Create index "idx_address_postal_codes_gin" to table: "address"
CREATE INDEX "idx_address_postal_codes_gin" ON "vcard"."address" USING gin ("postal_codes");
-- Create index "idx_address_regions_gin" to table: "address"
CREATE INDEX "idx_address_regions_gin" ON "vcard"."address" USING gin ("regions");
-- Create index "idx_address_rooms_gin" to table: "address"
CREATE INDEX "idx_address_rooms_gin" ON "vcard"."address" USING gin ("rooms");
-- Create index "idx_address_street_addresses_gin" to table: "address"
CREATE INDEX "idx_address_street_addresses_gin" ON "vcard"."address" USING gin ("street_addresses");
-- Create index "idx_address_street_numbers_gin" to table: "address"
CREATE INDEX "idx_address_street_numbers_gin" ON "vcard"."address" USING gin ("street_numbers");
-- Create index "idx_address_streets_gin" to table: "address"
CREATE INDEX "idx_address_streets_gin" ON "vcard"."address" USING gin ("streets");
-- Create index "idx_address_subdistricts_gin" to table: "address"
CREATE INDEX "idx_address_subdistricts_gin" ON "vcard"."address" USING gin ("subdistricts");
-- Create "contacts" table
CREATE TABLE "vcard"."contacts" ("id" text NOT NULL, "kind" text NULL, "display_names" text[] NOT NULL, "titles" text[] NULL, "notes" text[] NULL, "categories" text[] NULL, "default_language_code" text NULL, "name_components_id" text NULL, "birthday_id" text NULL, "anniversary_id" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_contacts_kind" CHECK (kind = ANY (ARRAY['INDIVIDUAL'::text, 'GROUP'::text, 'ORG'::text, 'LOCATION'::text])));
-- Create index "idx_contacts_anniversary_id" to table: "contacts"
CREATE INDEX "idx_contacts_anniversary_id" ON "vcard"."contacts" ("anniversary_id");
-- Create index "idx_contacts_birthday_id" to table: "contacts"
CREATE INDEX "idx_contacts_birthday_id" ON "vcard"."contacts" ("birthday_id");
-- Create index "idx_contacts_categories_gin" to table: "contacts"
CREATE INDEX "idx_contacts_categories_gin" ON "vcard"."contacts" USING gin ("categories");
-- Create index "idx_contacts_display_names_gin" to table: "contacts"
CREATE INDEX "idx_contacts_display_names_gin" ON "vcard"."contacts" USING gin ("display_names");
-- Create index "idx_contacts_name_components_id" to table: "contacts"
CREATE INDEX "idx_contacts_name_components_id" ON "vcard"."contacts" ("name_components_id");
-- Create index "idx_contacts_notes_gin" to table: "contacts"
CREATE INDEX "idx_contacts_notes_gin" ON "vcard"."contacts" USING gin ("notes");
-- Create index "idx_contacts_titles_gin" to table: "contacts"
CREATE INDEX "idx_contacts_titles_gin" ON "vcard"."contacts" USING gin ("titles");
-- Create "date_or_texts" table
CREATE TABLE "vcard"."date_or_texts" ("id" text NOT NULL, "date" date NULL, "date_time" timestamptz NULL, "date_time_zone" text NULL, "text" text NULL, "value_case" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_date_or_texts_value_case" CHECK (value_case = ANY (ARRAY['DATE'::text, 'DATE_TIME'::text, 'TEXT'::text])));
-- Create "emails" table
CREATE TABLE "vcard"."emails" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_emails_contact_id" to table: "emails"
CREATE INDEX "idx_emails_contact_id" ON "vcard"."emails" ("contact_id");
-- Create "extension_properties" table
CREATE TABLE "vcard"."extension_properties" ("id" text NOT NULL, "key" text NOT NULL, "values" text[] NULL, "parameters" jsonb NULL, "group" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_extension_properties_contact_id" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_contact_id" ON "vcard"."extension_properties" ("contact_id");
-- Create index "idx_extension_properties_values_gin" to table: "extension_properties"
CREATE INDEX "idx_extension_properties_values_gin" ON "vcard"."extension_properties" USING gin ("values");
-- Create "geos" table
CREATE TABLE "vcard"."geos" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_geos_contact_id" to table: "geos"
CREATE INDEX "idx_geos_contact_id" ON "vcard"."geos" ("contact_id");
-- Create "grammatical_genders" table
CREATE TABLE "vcard"."grammatical_genders" ("id" text NOT NULL, "kind" text NOT NULL DEFAULT 'ANIMATE', "language_code" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_grammatical_genders_kind" CHECK (kind = ANY (ARRAY['ANIMATE'::text, 'COMMON'::text, 'FEMININE'::text, 'INANIMATE'::text, 'MASCULINE'::text, 'NEUTER'::text])));
-- Create index "idx_grammatical_genders_contact_id" to table: "grammatical_genders"
CREATE INDEX "idx_grammatical_genders_contact_id" ON "vcard"."grammatical_genders" ("contact_id");
-- Create "instant_messages" table
CREATE TABLE "vcard"."instant_messages" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_instant_messages_contact_id" to table: "instant_messages"
CREATE INDEX "idx_instant_messages_contact_id" ON "vcard"."instant_messages" ("contact_id");
-- Create "languages" table
CREATE TABLE "vcard"."languages" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_languages_contact_id" to table: "languages"
CREATE INDEX "idx_languages_contact_id" ON "vcard"."languages" ("contact_id");
-- Create "names" table
CREATE TABLE "vcard"."names" ("id" text NOT NULL, "family_name" text[] NULL, "given_name" text[] NULL, "middle_names" text[] NULL, "honorific_prefixes" text[] NULL, "honorific_suffixes" text[] NULL, "secondary_surnames" text[] NULL, "generations" text[] NULL, "phonetic" text NULL, "script" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_names_phonetic" CHECK (phonetic = ANY (ARRAY['IPA'::text, 'PINY'::text, 'JYUT'::text, 'SCRIPT'::text])));
-- Create index "idx_names_family_name_gin" to table: "names"
CREATE INDEX "idx_names_family_name_gin" ON "vcard"."names" USING gin ("family_name");
-- Create index "idx_names_generations_gin" to table: "names"
CREATE INDEX "idx_names_generations_gin" ON "vcard"."names" USING gin ("generations");
-- Create index "idx_names_given_name_gin" to table: "names"
CREATE INDEX "idx_names_given_name_gin" ON "vcard"."names" USING gin ("given_name");
-- Create index "idx_names_honorific_prefixes_gin" to table: "names"
CREATE INDEX "idx_names_honorific_prefixes_gin" ON "vcard"."names" USING gin ("honorific_prefixes");
-- Create index "idx_names_honorific_suffixes_gin" to table: "names"
CREATE INDEX "idx_names_honorific_suffixes_gin" ON "vcard"."names" USING gin ("honorific_suffixes");
-- Create index "idx_names_middle_names_gin" to table: "names"
CREATE INDEX "idx_names_middle_names_gin" ON "vcard"."names" USING gin ("middle_names");
-- Create index "idx_names_secondary_surnames_gin" to table: "names"
CREATE INDEX "idx_names_secondary_surnames_gin" ON "vcard"."names" USING gin ("secondary_surnames");
-- Create "nicknames" table
CREATE TABLE "vcard"."nicknames" ("id" text NOT NULL, "values" text[] NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_nicknames_contact_id" to table: "nicknames"
CREATE INDEX "idx_nicknames_contact_id" ON "vcard"."nicknames" ("contact_id");
-- Create index "idx_nicknames_values_gin" to table: "nicknames"
CREATE INDEX "idx_nicknames_values_gin" ON "vcard"."nicknames" USING gin ("values");
-- Create "organizations" table
CREATE TABLE "vcard"."organizations" ("id" text NOT NULL, "value" text NOT NULL, "units" text[] NULL, "types" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_organizations_contact_id" to table: "organizations"
CREATE INDEX "idx_organizations_contact_id" ON "vcard"."organizations" ("contact_id");
-- Create index "idx_organizations_units_gin" to table: "organizations"
CREATE INDEX "idx_organizations_units_gin" ON "vcard"."organizations" USING gin ("units");
-- Create "pronouns" table
CREATE TABLE "vcard"."pronouns" ("id" text NOT NULL, "value" text NOT NULL, "language_code" text NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_pronouns_contact_id" to table: "pronouns"
CREATE INDEX "idx_pronouns_contact_id" ON "vcard"."pronouns" ("contact_id");
-- Create "relations" table
CREATE TABLE "vcard"."relations" ("id" text NOT NULL, "uri" text NULL, "text" text NULL, "relation_types" text NULL, "pref" integer NULL, "value_case" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_relations_value_case" CHECK (value_case = ANY (ARRAY['URI'::text, 'TEXT'::text])));
-- Create index "idx_relations_contact_id" to table: "relations"
CREATE INDEX "idx_relations_contact_id" ON "vcard"."relations" ("contact_id");
-- Create "resource" table
CREATE TABLE "vcard"."resource" ("id" text NOT NULL, "name" text NOT NULL, "uid" text NULL, "vcard_uid" text NULL, "vcard_create_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, "delete_time" timestamptz NULL, "expire_time" timestamptz NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_resource_contact_id" to table: "resource"
CREATE INDEX "idx_resource_contact_id" ON "vcard"."resource" ("contact_id");
-- Create index "idx_resource_delete_time" to table: "resource"
CREATE INDEX "idx_resource_delete_time" ON "vcard"."resource" ("delete_time");
-- Create index "idx_vcard_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_vcard_resource_name" ON "vcard"."resource" ("name");
-- Create index "idx_vcard_resource_uid" to table: "resource"
CREATE UNIQUE INDEX "idx_vcard_resource_uid" ON "vcard"."resource" ("uid");
-- Create "roles" table
CREATE TABLE "vcard"."roles" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_roles_contact_id" to table: "roles"
CREATE INDEX "idx_roles_contact_id" ON "vcard"."roles" ("contact_id");
-- Create "social_profiles" table
CREATE TABLE "vcard"."social_profiles" ("id" text NOT NULL, "uri" text NULL, "text" text NULL, "service_type" text NULL, "username" text NULL, "types" text NULL, "pref" integer NULL, "value_case" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_social_profiles_value_case" CHECK (value_case = ANY (ARRAY['URI'::text, 'TEXT'::text])));
-- Create index "idx_social_profiles_contact_id" to table: "social_profiles"
CREATE INDEX "idx_social_profiles_contact_id" ON "vcard"."social_profiles" ("contact_id");
-- Create "telephones" table
CREATE TABLE "vcard"."telephones" ("id" text NOT NULL, "uri" text NULL, "text" text NULL, "types" text NULL, "features" text NULL, "pref" integer NULL, "value_case" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_telephones_value_case" CHECK (value_case = ANY (ARRAY['URI'::text, 'TEXT'::text])));
-- Create index "idx_telephones_contact_id" to table: "telephones"
CREATE INDEX "idx_telephones_contact_id" ON "vcard"."telephones" ("contact_id");
-- Create "timezones" table
CREATE TABLE "vcard"."timezones" ("id" text NOT NULL, "text" text NULL, "uri" text NULL, "utc_offset" text NULL, "types" text NULL, "pref" integer NULL, "value_case" text NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_timezones_value_case" CHECK (value_case = ANY (ARRAY['TEXT'::text, 'URI'::text, 'UTC_OFFSET'::text])));
-- Create index "idx_timezones_contact_id" to table: "timezones"
CREATE INDEX "idx_timezones_contact_id" ON "vcard"."timezones" ("contact_id");
-- Create "urls" table
CREATE TABLE "vcard"."urls" ("id" text NOT NULL, "value" text NOT NULL, "types" text NULL, "pref" integer NULL, "contact_id" text NOT NULL, PRIMARY KEY ("id"));
-- Create index "idx_urls_contact_id" to table: "urls"
CREATE INDEX "idx_urls_contact_id" ON "vcard"."urls" ("contact_id");
-- Create "deliveries" table
CREATE TABLE "webhook"."deliveries" ("id" text NOT NULL, "name" text NOT NULL, "event_id" text NULL, "event_type" text NULL, "subject" text NULL, "payload" text NULL, "state" text NULL, "attempt_count" integer NULL, "next_attempt_time" timestamptz NULL, "response_code" integer NULL, "error_message" text NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "deliver_time" timestamptz NULL, "etag" text NULL, "webhook_id" text NOT NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_deliveries_state" CHECK (state = ANY (ARRAY['PENDING'::text, 'SUCCEEDED'::text, 'FAILED'::text])));
-- Create index "idx_deliveries_state_next_attempt_time" to table: "deliveries"
CREATE INDEX "idx_deliveries_state_next_attempt_time" ON "webhook"."deliveries" ("state", "next_attempt_time");
-- Create index "idx_deliveries_webhook_id_id" to table: "deliveries"
CREATE INDEX "idx_deliveries_webhook_id_id" ON "webhook"."deliveries" ("webhook_id", "id");
-- Create index "idx_webhook_deliveries_name" to table: "deliveries"
CREATE UNIQUE INDEX "idx_webhook_deliveries_name" ON "webhook"."deliveries" ("name");
-- Create "feed_cursors" table
CREATE TABLE "webhook"."feed_cursors" ("id" text NOT NULL, "name" text NOT NULL, "position_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, PRIMARY KEY ("id"));
-- Create index "idx_webhook_feed_cursors_name" to table: "feed_cursors"
CREATE UNIQUE INDEX "idx_webhook_feed_cursors_name" ON "webhook"."feed_cursors" ("name");
-- Create "resource" table
CREATE TABLE "webhook"."resource" ("id" text NOT NULL, "name" text NOT NULL, "display_name" text NULL, "uri" text NOT NULL, "event_types" text[] NULL, "organizational_unit" text NULL, "state" text NULL DEFAULT 'ACTIVE', "secret" text NULL, "previous_secret" text NULL, "rotate_time" timestamptz NULL, "create_time" timestamptz NOT NULL, "update_time" timestamptz NOT NULL, "etag" text NULL, PRIMARY KEY ("id"), CONSTRAINT "chk_resource_state" CHECK (state = ANY (ARRAY['ACTIVE'::text, 'PAUSED'::text])));
-- Create index "idx_resource_event_types_gin" to table: "resource"
CREATE INDEX "idx_resource_event_types_gin" ON "webhook"."resource" USING gin ("event_types");
-- Create index "idx_resource_organizational_unit" to table: "resource"
CREATE INDEX "idx_resource_organizational_unit" ON "webhook"."resource" ("organizational_unit");
-- Create index "idx_webhook_resource_name" to table: "resource"
CREATE UNIQUE INDEX "idx_webhook_resource_name" ON "webhook"."resource" ("name");
