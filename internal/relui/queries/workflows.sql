-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

-- name: Workflows :many
SELECT *
FROM workflows
ORDER BY created_at DESC;

-- name: Workflow :one
SELECT *
FROM workflows
WHERE id = $1;

-- name: CreateWorkflow :one
INSERT INTO workflows (id, params, name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateTask :one
INSERT INTO tasks (workflow_id, name, finished, result, error, created_at, updated_at, approved_at,
                   ready_for_approval)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpsertTask :one
INSERT INTO tasks (workflow_id, name, started, finished, result, error, created_at, updated_at, retry_count)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
ON CONFLICT (workflow_id, name) DO UPDATE
    SET workflow_id = excluded.workflow_id,
        name        = excluded.name,
        started     = excluded.started,
        finished    = excluded.finished,
        result      = excluded.result,
        error       = excluded.error,
        updated_at  = excluded.updated_at,
        retry_count = excluded.retry_count
RETURNING *;

-- name: Tasks :many
WITH most_recent_logs AS (
    SELECT workflow_id, task_name, MAX(updated_at) AS updated_at
    FROM task_logs
    GROUP BY workflow_id, task_name
)
SELECT tasks.*,
       GREATEST(most_recent_logs.updated_at, tasks.updated_at)::timestamptz AS most_recent_update
FROM tasks
LEFT JOIN most_recent_logs ON tasks.workflow_id = most_recent_logs.workflow_id AND
                              tasks.name = most_recent_logs.task_name
ORDER BY most_recent_update DESC;

-- name: TasksForWorkflowSorted :many
WITH most_recent_logs AS (
    SELECT workflow_id, task_name, MAX(updated_at) AS updated_at
    FROM task_logs
    GROUP BY workflow_id, task_name
)
SELECT tasks.*,
       GREATEST(most_recent_logs.updated_at, tasks.updated_at)::timestamptz AS most_recent_update
FROM tasks
LEFT JOIN most_recent_logs ON tasks.workflow_id = most_recent_logs.workflow_id AND
                              tasks.name = most_recent_logs.task_name
WHERE tasks.workflow_id = $1
ORDER BY most_recent_update DESC;

-- name: TasksForWorkflow :many
SELECT tasks.*
FROM tasks
WHERE workflow_id = $1
ORDER BY created_at;

-- name: Task :one
SELECT tasks.*
FROM tasks
WHERE workflow_id = $1
  AND name = $2
LIMIT 1;

-- name: CreateTaskLog :one
INSERT INTO task_logs (workflow_id, task_name, body)
VALUES ($1, $2, $3)
RETURNING *;

-- name: TaskLogsForTask :many
SELECT task_logs.*
FROM task_logs
WHERE workflow_id = $1
  AND task_name = $2
ORDER BY created_at;

-- name: TaskLogsForWorkflow :many
SELECT task_logs.*
FROM task_logs
WHERE workflow_id = $1
ORDER BY created_at;

-- name: TaskLogs :many
SELECT task_logs.*
FROM task_logs
ORDER BY created_at;

-- name: UnfinishedWorkflows :many
SELECT workflows.*
FROM workflows
WHERE workflows.finished = FALSE;

-- name: WorkflowFinished :one
UPDATE workflows
SET finished   = $2,
    output     = $3,
    error      = $4,
    updated_at = $5
WHERE workflows.id = $1
RETURNING *;

-- name: ResetTask :one
UPDATE tasks
SET finished    = FALSE,
    started     = FALSE,
    approved_at = DEFAULT,
    result      = DEFAULT,
    error       = DEFAULT,
    updated_at  = $3
WHERE workflow_id = $1
  AND name = $2
RETURNING *;

-- name: ResetWorkflow :one
UPDATE workflows
SET finished   = FALSE,
    output     = DEFAULT,
    error      = DEFAULT,
    updated_at = $2
WHERE id = $1
RETURNING *;

-- name: ApproveTask :one
UPDATE tasks
SET approved_at = $3,
    updated_at  = $3
WHERE workflow_id = $1
  AND name = $2
RETURNING *;

-- name: UpdateTaskReadyForApproval :one
UPDATE tasks
SET ready_for_approval = $3
WHERE workflow_id = $1
  AND name = $2
RETURNING *;
