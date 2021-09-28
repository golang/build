-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

CREATE TABLE task_logs (
  id SERIAL PRIMARY KEY,
  workflow_id uuid NOT NULL,
  task_name text NOT NULL,
  body text NOT NULL,
  created_at timestamp with time zone NOT NULL default current_timestamp,
  updated_at timestamp with time zone NOT NULL default current_timestamp,
  FOREIGN KEY (workflow_id, task_name) REFERENCES tasks (workflow_id, name)
);
