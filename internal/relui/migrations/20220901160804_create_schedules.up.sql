--  Copyright 2022 The Go Authors. All rights reserved.
--  Use of this source code is governed by a BSD-style
--  license that can be found in the LICENSE file.

CREATE TABLE schedules
(
    id               SERIAL PRIMARY KEY,
    workflow_name    text                     NOT NULL,
    workflow_params  jsonb,
    spec             text                     NOT NULL,
    once             timestamp WITH TIME ZONE,
    interval_minutes int                      NOT NULL,
    created_at       timestamp WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at       timestamp WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

ALTER TABLE workflows
    ADD COLUMN schedule_id integer REFERENCES schedules (id);

CREATE INDEX workflows_schedule_id_ix ON workflows (schedule_id) WHERE workflows.schedule_id IS NOT NULL;
