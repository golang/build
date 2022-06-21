-- Copyright 2022 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

BEGIN;

ALTER TABLE tasks
    ADD COLUMN approved_at timestamp WITH TIME ZONE NULL;

WITH approved_tasks AS (
    SELECT workflow_id, task_name, max(created_at) AS created_at
    FROM task_logs
    WHERE body = 'USER-APPROVED'
    GROUP BY workflow_id, task_name
)

UPDATE tasks
SET approved_at = approved_tasks.created_at
FROM approved_tasks
WHERE tasks.workflow_id = approved_tasks.workflow_id
  AND tasks.name = approved_tasks.task_name;

COMMIT;
