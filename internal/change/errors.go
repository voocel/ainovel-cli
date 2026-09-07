package change

import "errors"

var (
	ErrUnauthorized        = errors.New("change is not authorized")
	ErrStructuralConflict  = errors.New("structural story conflict")
	ErrInvalidState        = errors.New("invalid change state")
	ErrSemanticUnavailable = errors.New("semantic analysis is unavailable")
)
