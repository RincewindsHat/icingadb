package icingadb

import (
	"context"
	"fmt"
	"github.com/icinga/icingadb/internal"
	"github.com/icinga/icingadb/pkg/backoff"
	"github.com/icinga/icingadb/pkg/com"
	"github.com/icinga/icingadb/pkg/retry"
	"github.com/icinga/icingadb/pkg/types"
	"time"
)

// CleanupStmt defines information needed to compose cleanup statements.
type CleanupStmt struct {
	Table  string
	PK     string
	Column string
}

// Build assembles the cleanup statement for the specified database driver with the given limit.
func (stmt *CleanupStmt) Build(driverName string, limit uint64) string {
	switch driverName {
	case MySQL:
		return fmt.Sprintf(`DELETE FROM %[1]s WHERE environment_id = :environment_id AND %[2]s < :time
ORDER BY %[2]s LIMIT %[3]d`, stmt.Table, stmt.Column, limit)
	case PostgreSQL:
		return fmt.Sprintf(`WITH rows AS (
SELECT %[1]s FROM %[2]s WHERE environment_id = :environment_id AND %[3]s < :time ORDER BY %[3]s LIMIT %[4]d
)
DELETE FROM %[2]s WHERE %[1]s IN (SELECT %[1]s FROM rows)`, stmt.PK, stmt.Table, stmt.Column, limit)
	default:
		panic(fmt.Sprintf("invalid database type %s", driverName))
	}
}

// CleanupOlderThan deletes all rows with the specified statement that are older than the given time.
// Deletes a maximum of as many rows per round as defined in count. Actually deleted rows will be passed to onSuccess.
// Returns the total number of rows deleted.
func (db *DB) CleanupOlderThan(
	ctx context.Context, stmt CleanupStmt, envId types.Binary,
	count uint64, olderThan time.Time, onSuccess ...OnSuccess[struct{}],
) (uint64, error) {
	var counter com.Counter

	q := db.Rebind(stmt.Build(db.DriverName(), count))

	defer db.log(ctx, q, &counter).Stop()

	for {
		var rowsDeleted int64

		err := retry.WithBackoff(
			ctx,
			func(ctx context.Context) error {
				rs, err := db.NamedExecContext(ctx, q, cleanupWhere{
					EnvironmentId: envId,
					Time:          types.UnixMilli(olderThan),
				})
				if err != nil {
					return internal.CantPerformQuery(err, q)
				}

				rowsDeleted, err = rs.RowsAffected()

				return err
			},
			retry.Retryable,
			backoff.NewExponentialWithJitter(1*time.Millisecond, 1*time.Second),
			db.getDefaultRetrySettings(),
		)
		if err != nil {
			return 0, err
		}

		counter.Add(uint64(rowsDeleted))

		for _, onSuccess := range onSuccess {
			if err := onSuccess(ctx, make([]struct{}, rowsDeleted)); err != nil {
				return 0, err
			}
		}

		if rowsDeleted < int64(count) {
			break
		}
	}

	return counter.Total(), nil
}

type cleanupWhere struct {
	EnvironmentId types.Binary
	Time          types.UnixMilli
}
