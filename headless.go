package main

import (
	"context"
	"fmt"
	"time"

	"github.com/spotbye/SpotiFLAC/backend"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// Headless mode: a companion app (soniq8-DJ) starts SpotiFLAC with --headless
// to download missing tracks. The app then serves its download API with no
// window and no Dock icon, and quits once it has been idle for
// headlessIdleLimit. The window still opens when a provider asks the user to
// verify.

const headlessFlag = "--headless"

// headlessIdleLimit is how long a headless run waits with no download request
// and nothing queued before it quits. The companion starts it again when needed.
const headlessIdleLimit = 10 * time.Minute

// isHeadless reports whether the command-line arguments ask for headless mode.
func isHeadless(args []string) bool {
	for _, a := range args {
		if a == headlessFlag {
			return true
		}
	}
	return false
}

// idleTooLong is true when nothing is queued or downloading and the last
// activity is at least limit ago.
func idleTooLong(last, now time.Time, busy bool, limit time.Duration) bool {
	return !busy && now.Sub(last) >= limit
}

// startHeadless keeps a headless run out of the Dock and quits it once it has
// been idle. A run that could not open the download API has nothing to do, so
// it quits at once rather than linger unseen.
func (a *App) startHeadless(ctx context.Context) {
	hideFromDock()
	if a.api == nil || !a.api.listening() {
		fmt.Println("[headless] download API not available, quitting")
		runtime.Quit(ctx)
		return
	}
	go func() {
		tick := time.NewTicker(30 * time.Second)
		defer tick.Stop()
		for range tick.C {
			// Nobody clears the queue in headless mode, so finished work is forgotten here.
			if a.api.pruneFinished(headlessIdleLimit) > 0 {
				backend.ClearDownloadQueue()
			}
			last, busy := a.api.activity()
			if idleTooLong(last, time.Now(), busy, headlessIdleLimit) {
				fmt.Printf("[headless] idle for %s, quitting\n", headlessIdleLimit)
				runtime.Quit(ctx)
				return
			}
		}
	}()
}
