package main

import (
	"context"

	"github.com/ryanmwright/careen/internal/contract"
	"github.com/ryanmwright/careen/internal/execx"
	"github.com/ryanmwright/careen/internal/inventory"
	"github.com/ryanmwright/careen/internal/repo"
)

// openRepo opens the writable checkout: --repo, $CAREEN_REPO, or the
// usual places.
func openRepo(ctx context.Context, g *globalFlags) (*repo.Repo, error) {
	dir, err := repo.Find(g.repo)
	if err != nil {
		return nil, contract.Usage("%v", err)
	}
	rp, err := repo.Open(ctx, execx.Run, dir)
	if err != nil {
		return nil, contract.Failed("repo", "%v", err)
	}
	return rp, nil
}

// optionalInventory is the fleet if --flake is set and loads, else nil;
// key and secret commands work from the checkout alone.
func optionalInventory(ctx context.Context, g *globalFlags) *inventory.Inventory {
	if g.flake == "" {
		return nil
	}
	inv, err := inventory.Load(ctx, execx.Run, g.flake, false)
	if err != nil {
		return nil
	}
	return inv
}

func hostAddress(inv *inventory.Inventory, host string) string {
	if inv != nil {
		for _, h := range inv.Hosts {
			if h.Name == host {
				return h.Address
			}
		}
	}
	return ""
}
