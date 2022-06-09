-- Copyright 2021 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

BEGIN;

DELETE
FROM task_logs
WHERE workflow_id = 'e7cb64ec-91b1-47fa-b962-2b2f034c458c';

DELETE
FROM tasks
WHERE workflow_id = 'e7cb64ec-91b1-47fa-b962-2b2f034c458c';

DELETE
FROM workflows
WHERE id = 'e7cb64ec-91b1-47fa-b962-2b2f034c458c';

COMMIT;
