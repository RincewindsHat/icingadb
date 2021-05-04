package main

import (
	"context"
	"fmt"
	"github.com/icinga/icingadb/internal/command"
	"github.com/icinga/icingadb/pkg/icingadb"
	"github.com/icinga/icingadb/pkg/icingadb/history"
	v1 "github.com/icinga/icingadb/pkg/icingadb/v1"
	"github.com/icinga/icingadb/pkg/icingaredis"
	"github.com/icinga/icingadb/pkg/utils"
	"github.com/pkg/errors"
	"go.uber.org/zap"
	"golang.org/x/sync/errgroup"
	"os"
	"os/signal"
	"syscall"
)

func main() {
	cmd := command.New()
	logger := cmd.Logger
	defer logger.Sync()
	defer func() {
		if err := recover(); err != nil {
			type stackTracer interface {
				StackTrace() errors.StackTrace
			}
			if err, ok := err.(stackTracer); ok {
				for _, f := range err.StackTrace() {
					fmt.Printf("%+s:%d\n", f, f)
				}
			}
		}
	}()
	db := cmd.Database()
	defer db.Close()
	rc := cmd.Redis()

	ctx, cancelCtx := context.WithCancel(context.Background())
	heartbeat := icingaredis.NewHeartbeat(ctx, rc, logger)
	ha := icingadb.NewHA(ctx, db, heartbeat, logger)
	defer ha.Close()
	s := icingadb.NewSync(db, rc, logger)
	hs := history.NewSync(db, rc, logger)
	rt := icingadb.NewRuntimeUpdates(db, rc, logger)

	sig := make(chan os.Signal)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)

	// Main loop
	for {
		hactx, cancelHactx := context.WithCancel(ctx)
		for hactx.Err() == nil {
			select {
			case <-ha.Takeover():
				go func() {
					for hactx.Err() == nil {
						synctx, cancelSynctx := context.WithCancel(hactx)
						g, synctx := errgroup.WithContext(synctx)

						dump := icingadb.NewDumpSignals(rc, logger)
						g.Go(func() error {
							return dump.Listen(synctx)
						})

						lastRuntimeStreamId, err := rc.StreamLastId(ctx, "icinga:runtime")
						if err != nil {
							panic(err)
						}

						g.Go(func() error {
							select {
							case <-dump.InProgress():
								logger.Info("Icinga 2 started a new config dump, waiting for it to complete")
								cancelSynctx()
								return nil
							case <-synctx.Done():
								return synctx.Err()
							}
						})

						g.Go(func() error {
							return hs.Sync(synctx)
						})

						g.Go(func() error {
							return rt.Sync(synctx, v1.Factories, lastRuntimeStreamId)
						})

						for _, factory := range v1.Factories {
							factory := factory

							g.Go(func() error {
								return s.SyncAfterDump(synctx, factory.WithInit, dump)
							})
						}

						if err := g.Wait(); err != nil && !utils.IsContextCanceled(err) {
							panic(err)
						}
					}
				}()
			case <-ha.Handover():
				cancelHactx()
			case <-hactx.Done():
				// Nothing to do here, surrounding loop will terminate now.
			case <-ctx.Done():
				if err := ctx.Err(); err != nil && !utils.IsContextCanceled(err) {
					panic(err)
				}
				return
			case s := <-sig:
				logger.Infow("Exiting due to signal", zap.String("signal", s.String()))
				cancelCtx()
				return
			}
		}
	}
}
