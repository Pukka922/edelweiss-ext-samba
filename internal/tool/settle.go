package tool

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/setup"
)

// The defaults of settle: how often it looks, and when it gives up.
const (
	settlePoll  = 200 * time.Millisecond
	settleLimit = 60 * time.Second
)

// settle waits until OpenRC is starting or stopping none of dbus, avahi-daemon, samba and wsdd (setup.Transitioning): a
// rc-service command on a service in transition fails with "is already starting", as it does for wsdd right after
// rc-service samba restart, which has OpenRC start wsdd again in the background. It looks every 200 ms and gives up after
// 60 s; on the deadline it ends with 0 as well, with a line on stderr that names what is still in transition, so that
// the next rc-service step reports the real failure. A cancel ends it with StatusFailed.
func (t Tool) settle(ctx context.Context, args []string) error {
	if err := parse(newFlags("settle"), args); err != nil {
		return err
	}
	poll, limit := t.SettlePoll, t.SettleLimit
	if poll <= 0 {
		poll = settlePoll
	}
	if limit <= 0 {
		limit = settleLimit
	}
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(poll)
	defer tick.Stop()
	for {
		busy := setup.Transitioning(t.FS)
		if len(busy) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("cancelled while %s in transition", strings.Join(busy, ", "))
		case <-deadline.C:
			fmt.Fprintf(t.Stderr, "settle: still in transition after %s: %s\n", limit, strings.Join(busy, ", "))
			return nil
		case <-tick.C:
		}
	}
}
