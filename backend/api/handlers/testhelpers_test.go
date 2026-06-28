package handlers_test

import (
	"context"
	"errors"

	"cantus/backend/services"
)

// errStorage wraps a real Storage but returns an error on Has for a specific key
// suffix. errOnHasName is matched against the tail of the key.
type errStorage struct {
	services.Storage
	errOnHasName string
}

func (e *errStorage) Has(ctx context.Context, key string) (bool, error) {
	if e.errOnHasName != "" && len(key) >= len(e.errOnHasName) &&
		key[len(key)-len(e.errOnHasName):] == e.errOnHasName {
		return false, errors.New("storage exploded")
	}
	return e.Storage.Has(ctx, key)
}
