-- Copyright 2022 The Go Authors. All rights reserved.
-- Use of this source code is governed by a BSD-style
-- license that can be found in the LICENSE file.

-- Back-filling the name prefix would break workflows, as names are
-- significant.
ALTER TABLE tasks DROP COLUMN ready_for_approval;
