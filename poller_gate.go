package mineskin

import "errors"

// errCheckSkipped is returned by a request whose start gate declined it. The
// poller treats it as "nothing to do", never as a job failure.
var errCheckSkipped = errors.New("mineskin: request skipped by its start gate")
