-- Copyright 2022 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

BEGIN;

WITH approved_task_logs AS (
    SELECT DISTINCT workflow_id, task_name
    FROM task_logs
    WHERE body = 'USER-APPROVED'
)

INSERT
INTO task_logs (workflow_id, task_name, body, created_at, updated_at)
SELECT tasks.workflow_id, tasks.name, 'USER-APPROVED', tasks.approved_at, tasks.approved_at
FROM tasks
WHERE tasks.approved_at IS NOT NULL
  AND NOT EXISTS(
        SELECT DISTINCT 1
        FROM approved_task_logs atl
        WHERE atl.workflow_id = tasks.workflow_id
          AND atl.task_name = tasks.name
    )
;

ALTER TABLE tasks
    DROP COLUMN approved_at;

COMMIT;
