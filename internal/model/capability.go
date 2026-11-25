package model

import "errors"

// ErrNoCapability is the cause when a forge cannot do what a caller asks. A caller tests for it with errors.Is.
// The package forge names the same value, so a backend and the merge see one sentinel.
var ErrNoCapability = errors.New("the forge cannot do this")
