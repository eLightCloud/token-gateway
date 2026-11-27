package model

import (
	"errors"
	"fmt"
)

var ErrResellerUsernameConflict = fmt.Errorf("username already in use: %w", ErrResellerDuplicateResource)

func resellerCreateError(username string, err error) error {
	if !errors.Is(err, ErrResellerDuplicateResource) {
		return err
	}
	// Check after rollback so the lookup only sees usernames held by other accounts,
	// including soft-deleted accounts that still reserve their usernames.
	if exists, lookupErr := CheckUserExistOrDeleted(username, ""); lookupErr == nil && exists {
		return ErrResellerUsernameConflict
	}
	return err
}
