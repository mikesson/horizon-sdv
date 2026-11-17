-- Copyright 2026 Google LLC
--
-- Licensed under the Apache License, Version 2.0 (the "License");
-- you may not use this file except in compliance with the License.
-- You may obtain a copy of the License at
--
--     https://www.apache.org/licenses/LICENSE-2.0
--
-- Unless required by applicable law or agreed to in writing, software
-- distributed under the License is distributed on an "AS IS" BASIS,
-- WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
-- See the License for the specific language governing permissions and
-- limitations under the License.

-- Upgrades a v86 database to v87. It drops columns nothing reads or writes.

-- Before deploying the v87 binary, which refuses a database older than v87.
-- Apply with `gcloud spanner databases execute-sql`.
UPDATE system_config SET config_value = '87', updated_at = CURRENT_TIMESTAMP()
  WHERE config_key = 'schema.version';

-- After the v87 binary serves, since the v86 binary reads these columns.
-- Apply with `gcloud spanner databases ddl update`.
ALTER TABLE devices DROP COLUMN vnc_port;
ALTER TABLE devices DROP COLUMN build_source;
ALTER TABLE devices DROP COLUMN gcs_uris;
ALTER TABLE devices DROP COLUMN generation;
ALTER TABLE devices DROP COLUMN observed_generation;
ALTER TABLE audit_logs DROP COLUMN actor_id;
ALTER TABLE audit_logs DROP COLUMN actor_ip;
