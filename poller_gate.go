package mineskin

import "github.com/IvanNovR/mineskin-go-client/internal/ratelimit"

// errCheckSkipped is returned by a request whose start gate declined it. The
// poller treats it as "nothing to do", never as a job failure.
var errCheckSkipped = ratelimit.ErrSkipped
