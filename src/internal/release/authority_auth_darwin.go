//go:build darwin && cgo

package release

/*
#cgo LDFLAGS: -framework Security
#include <Security/Authorization.h>
#include <Security/AuthorizationTags.h>
#include <stdlib.h>
#include <string.h>

static OSStatus rr_authorize_release_mandate(const char *prompt) {
	AuthorizationRef authorization = NULL;
	OSStatus status = AuthorizationCreate(NULL, kAuthorizationEmptyEnvironment,
		kAuthorizationFlagDefaults, &authorization);
	if (status != errAuthorizationSuccess) return status;

	AuthorizationItem right = { kAuthorizationRightExecute, 0, NULL, 0 };
	AuthorizationRights rights = { 1, &right };
	AuthorizationItem promptItem = { kAuthorizationEnvironmentPrompt, strlen(prompt), (void *)prompt, 0 };
	AuthorizationEnvironment environment = { 1, &promptItem };
	AuthorizationFlags flags = kAuthorizationFlagInteractionAllowed |
		kAuthorizationFlagExtendRights | kAuthorizationFlagPreAuthorize;
	status = AuthorizationCopyRights(authorization, &rights, &environment, flags, NULL);
	AuthorizationFree(authorization, kAuthorizationFlagDestroyRights);
	return status;
}
*/
import "C"

import (
	"context"
	"errors"
	"strings"
	"unsafe"
)

// NewSystemAuthorityAuthorizer uses macOS Authorization Services to request
// the interactive administrator right for one displayed body/context digest.
// It does not return or retain the authorization credential.
func NewSystemAuthorityAuthorizer() (InteractiveAuthorityAuthorizer, error) {
	return systemAuthorityAuthorizer{}, nil
}

type systemAuthorityAuthorizer struct{}

func (systemAuthorityAuthorizer) AuthorizeAuthorityMandate(ctx context.Context, description string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", ErrAuthorityDenied
	}
	description = strings.TrimSpace(description)
	if description == "" || len(description) > 768 || strings.ContainsRune(description, '\x00') {
		return "", ErrAuthorityDenied
	}
	prompt := C.CString(description)
	defer C.free(unsafe.Pointer(prompt))
	if status := C.rr_authorize_release_mandate(prompt); status != C.errAuthorizationSuccess {
		return "", ErrAuthorityDenied
	}
	receipt, err := newAuthorityID()
	if err != nil {
		return "", errors.Join(ErrAuthorityDenied, err)
	}
	return "mac-admin-" + receipt, nil
}
