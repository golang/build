-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

-- name: Workflows :many
SELECT id, params, name, created_at, updated_at
FROM workflows
ORDER BY created_at DESC;

-- name: Workflow :one
SELECT id, params, name, created_at, updated_at
FROM workflows
WHERE id = $1;

-- name: CreateWorkflow :one
INSERT INTO workflows (id, params, name, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- name: CreateTask :one
INSERT INTO tasks (workflow_id, name, finished, result, error, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING *;

-- name: UpsertTask :one
INSERT INTO tasks (workflow_id, name, finished, result, error, created_at, updated_at)
VALUES ($1, $2, $3, $4, $5, $6, $7)
ON CONFLICT (workflow_id, name) DO UPDATE
    SET workflow_id = excluded.workflow_id,
        name        = excluded.name,
        finished    = excluded.finished,
        result      = excluded.result,
        updated_at  = excluded.updated_at
RETURNING *;

-- name: Tasks :many
SELECT tasks.*
FROM tasks
ORDER BY created_at;

-- name: TasksForWorkflow :many
SELECT tasks.*
FROM tasks
WHERE workflow_id=$1
ORDER BY created_at;

-- name: CreateTaskLog :one
INSERT INTO task_logs (workflow_id, task_name, body)
VALUES ($1, $2, $3)
RETURNING *;

-- name: TaskLogsForTask :many
SELECT task_logs.*
FROM task_logs
WHERE workflow_id=$1 AND task_name = $2
ORDER BY created_at;

-- name: TaskLogs :many
SELECT task_logs.*
FROM task_logs
ORDER BY created_at;
