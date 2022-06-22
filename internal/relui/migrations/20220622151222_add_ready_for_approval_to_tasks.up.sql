-- Copyright 2022 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

ALTER TABLE tasks
    ADD COLUMN ready_for_approval bool NOT NULL DEFAULT FALSE;

UPDATE tasks
SET ready_for_approval = TRUE
WHERE SUBSTRING(name FROM '^APPROVE-') != '';
