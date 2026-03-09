package sqlite

import "github.com/open-nipper/open-nipper/internal/datastore"

// Compile-time assertion that *Store implements datastore.Repository.
var _ datastore.Repository = (*Store)(nil)
