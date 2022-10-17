-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

-- name: Workflows :many
SELECT *
FROM workflows
ORDER BY created_at DESC;

-- name: WorkflowsByName :many
SELECT *
FROM workflows
WHERE name = $1
ORDER BY created_at DESC;

-- name: WorkflowsByNames :many
SELECT *
FROM workflows
WHERE name = ANY(@names::text[])
ORDER BY created_at DESC;

-- name: WorkflowNames :many
SELECT DISTINCT name::text
FROM workflows;

-- name: Workflow :one
SELECT *
FROM workflows
WHERE id = $1;

-- name: WorkflowCount :one
SELECT COUNT(*)
FROM workflows;

-- name: WorkflowSidebar :many
SELECT name, COUNT(*)
FROM workflows
GROUP BY name
ORDER BY name;

-- name: CreateWorkflow :one
INSERT INTO workflows (id, params, name, schedule_id, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING *;

-- name: CreateTask :one
INSERT INTO tasks (workflow_id, name, finished, result, error, created_at, updated_at, approved_at,
                   ready_for_approval)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
RETURNING *;

-- name: UpsertTask :one
INSERT INTO tasks (workflow_id, name, started, finished, result, error, created_at, updated_at,
                   retry_count)
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

-- name: FailUnfinishedTasks :exec
UPDATE tasks
    SET finished = TRUE,
    started      = TRUE,
    error        = 'task interrupted before completion',
    updated_at   = $2
WHERE workflow_id = $1 and started and not finished;

-- name: WorkflowFinished :one
UPDATE workflows
SET finished   = $2,
    output     = $3,
    error      = $4,
    updated_at = $5
WHERE workflows.id = $1
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

-- name: Schedules :many
SELECT *
FROM schedules
ORDER BY id;

-- name: CreateSchedule :one
INSERT INTO schedules (workflow_name, workflow_params, spec, once, interval_minutes, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: DeleteSchedule :one
DELETE
FROM schedules
WHERE id = $1
RETURNING *;

-- name: ClearWorkflowSchedule :many
UPDATE workflows
SET schedule_id = NULL
WHERE schedule_id = $1::int
RETURNING id;

-- name: SchedulesLastRun :many
WITH last_scheduled_run AS (
    SELECT DISTINCT ON (schedule_id) schedule_id, id, created_at, workflows.error, finished
    FROM workflows
    ORDER BY schedule_id, workflows.created_at DESC
)
SELECT schedules.id,
       last_scheduled_run.id AS workflow_id,
       last_scheduled_run.created_at AS workflow_created_at,
       last_scheduled_run.error AS workflow_error,
       last_scheduled_run.finished AS workflow_finished
FROM schedules
LEFT OUTER JOIN last_scheduled_run ON last_scheduled_run.schedule_id = schedules.id;
