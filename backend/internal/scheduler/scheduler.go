// Package scheduler runs the background jobs: the hourly Zoho sync, the Zoho payment read-back every two hours,
// and the minute-by-minute check for vendor order sheets due to go out automatically.
package scheduler

import (
	"context"
	"log"
	"time"
)

type Job struct {
	Name  string
	Every time.Duration
	Run   func(ctx context.Context) error
}

// Start runs each job on its interval until ctx ends. The first run happens one interval after start.
func Start(ctx context.Context, jobs ...Job) {
	for _, j := range jobs {
		go func(j Job) {
			t := time.NewTicker(j.Every)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					rc, cancel := context.WithTimeout(ctx, j.Every)
					if err := j.Run(rc); err != nil {
						log.Printf("job %s: %v", j.Name, err)
					}
					cancel()
				}
			}
		}(j)
	}
}
