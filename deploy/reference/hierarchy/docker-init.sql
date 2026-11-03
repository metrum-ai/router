-- Copyright 2026 Metrum AI
-- SPDX-License-Identifier: Apache-2.0
--
-- One usage database per hop. The edge database is created by the image's
-- POSTGRES_DB; this script adds the datacenter tier's database.
CREATE DATABASE dc_usage;
