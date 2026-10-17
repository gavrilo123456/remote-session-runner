//go:build !darwin || !cgo

package release

func NewSystemAuthorityAuthorizer() (InteractiveAuthorityAuthorizer, error) {
	return nil, ErrAuthorityStateUnavailable
}
