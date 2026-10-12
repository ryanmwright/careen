package actions

import (
	"context"
	"time"

	"github.com/ryanmwright/careen/internal/activate"
	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/inventory"
)

type RollbackDone struct {
	Host   string          `json:"host"`
	Result activate.Result `json:"result"`
}

// Rollback switches h to its previous generation and checks its health.
func Rollback(ctx context.Context, em *contract.Emitter, h inventory.Host, healthTimeout time.Duration) (RollbackDone, error) {
	em.Started("rollback", map[string]string{"host": h.Name})
	res, err := activate.RollbackGeneration(ctx, Conn(h, false), Conn(h, true), healthTimeout, func(step, msg string) {
		em.Progress(h.Name, step, "%s", msg)
	})
	if err != nil {
		return RollbackDone{Host: h.Name, Result: res}, contract.Failed("rollback", "%s: %v", h.Name, err)
	}
	done := RollbackDone{Host: h.Name, Result: res}
	em.Done(done)
	return done, nil
}
