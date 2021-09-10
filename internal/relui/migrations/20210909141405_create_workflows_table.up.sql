-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

CREATE TABLE workflows (
    id uuid PRIMARY KEY,
    params jsonb,
    name text,
    created_at timestamp with time zone NOT NULL default current_timestamp,
    updated_at timestamp with time zone NOT NULL default current_timestamp
);

CREATE TABLE tasks (
    workflow_id uuid REFERENCES workflows (id),
    name text,
    finished bool NOT NULL DEFAULT false,
    result jsonb,
    error text,
    created_at timestamp with time zone NOT NULl default current_timestamp,
    updated_at timestamp with time zone NOT NULL default current_timestamp,
    PRIMARY KEY (workflow_id, name)
);
