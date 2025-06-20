package history

import (
	"github.com/icinga/icinga-go-library/redis"
	"github.com/icinga/icinga-go-library/structify"
	"github.com/icinga/icingadb/pkg/common"
	"github.com/icinga/icingadb/pkg/contracts"
	"github.com/icinga/icingadb/pkg/icingadb/v1/history"
	"reflect"
)

var slaStateStructify = structify.MakeMapStructifier(
	reflect.TypeOf((*history.SlaHistoryState)(nil)).Elem(),
	"json",
	contracts.SafeInit)

func stateHistoryToSlaEntity(entry redis.XMessage) ([]history.UpserterEntity, error) {
	slaStateInterface, err := slaStateStructify(entry.Values)
	if err != nil {
		return nil, err
	}
	slaState := slaStateInterface.(*history.SlaHistoryState)

	if slaState.StateType != common.HardState {
		// only hard state changes are relevant for SLA history, discard all others
		return nil, nil
	}

	return []history.UpserterEntity{slaState}, nil
}
