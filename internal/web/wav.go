package web

import "github.com/rossnoah/radiobot/internal/wavutil"

// wavDuration is the fallback for recordings whose duration is not yet cached
// in the database.
func wavDuration(path string) (float64, error) { return wavutil.Duration(path) }
