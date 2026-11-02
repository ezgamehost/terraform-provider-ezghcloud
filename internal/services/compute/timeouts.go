package compute

import (
	"fmt"
	"time"
)

func parseTimeout(s string) (time.Duration, error) {
	d, e := time.ParseDuration(s)
	if e != nil || d <= 0 {
		return 0, fmt.Errorf("operation_timeout must be a positive duration, for example 30m")
	}
	return d, nil
}
