-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

BEGIN;

ALTER TABLE workflows
    ADD COLUMN finished bool NOT NULL DEFAULT false;

CREATE INDEX workflows_finished_ix ON workflows (finished) WHERE finished = false;

ALTER TABLE workflows
    ADD COLUMN output jsonb NOT NULL DEFAULT jsonb_build_object();

ALTER TABLE workflows
    ADD COLUMN error text NOT NULL DEFAULT '';

UPDATE workflows
SET finished = true
WHERE workflows.id NOT IN (
    SELECT DISTINCT tasks.workflow_id
    FROM tasks
    WHERE finished = false
);

COMMIT;
