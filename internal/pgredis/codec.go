package pgredis

import (
	"database/sql"
	"encoding/json"
	"errors"
)

func jsonMarshal(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}

// isNoRows maps the driver's "nothing matched" sentinel onto the package's
// convention that absent state is not an error.
func isNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// rowsAffected normalises a driver that does not report a count to zero, so
// callers compare against a plain bool rather than a -1 sentinel.
func rowsAffected(result sql.Result) int64 {
	if result == nil {
		return 0
	}
	count, err := result.RowsAffected()
	if err != nil || count < 0 {
		return 0
	}
	return count
}
