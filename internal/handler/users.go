package handler

import (
	"context"
	"errors"
	"net/http"
	"redlaunch/internal/application"
	"redlaunch/internal/auth"
	"strconv"
)

type userManagementService interface {
	List(context.Context) ([]application.UserAccount, error)
	Create(context.Context, string, string) error
	ChangePassword(context.Context, int64, string) error
	Delete(context.Context, int64) error
	AssignEmail(context.Context, int64, string) error
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) { h.mutateUser(w, r, "create") }
func (h *Handler) changeUserPassword(w http.ResponseWriter, r *http.Request) {
	h.mutateUser(w, r, "password")
}
func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) { h.mutateUser(w, r, "delete") }
func (h *Handler) assignUserEmail(w http.ResponseWriter, r *http.Request) {
	h.mutateUser(w, r, "email")
}

func (h *Handler) mutateUser(w http.ResponseWriter, r *http.Request, action string) {
	if h.users == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseBoundedForm(w, r); err != nil {
		http.Error(w, "Invalid user request.", http.StatusBadRequest)
		return
	}
	if !h.validRequestCSRF(r) {
		http.Error(w, "This settings page expired. Refresh and try again.", http.StatusForbidden)
		return
	}
	id, parseErr := strconv.ParseInt(r.Form.Get("id"), 10, 64)
	var err error
	if action != "create" && (parseErr != nil || id < 1) {
		err = application.ErrUserNotFound
	} else {
		switch action {
		case "create":
			if r.Form.Get("password") != r.Form.Get("password_confirmation") {
				err = errPasswordConfirmation
			} else {
				err = h.users.Create(r.Context(), r.Form.Get("email"), r.Form.Get("password"))
			}
		case "password":
			if r.Form.Get("password") != r.Form.Get("password_confirmation") {
				err = errPasswordConfirmation
			} else {
				err = h.users.ChangePassword(r.Context(), id, r.Form.Get("password"))
			}
		case "email":
			err = h.users.AssignEmail(r.Context(), id, r.Form.Get("email"))
		case "delete":
			if r.Form.Get("confirm") != "yes" {
				err = errUserConfirmation
			} else {
				err = h.users.Delete(r.Context(), id)
			}
		}
	}
	if err == nil {
		// Password/email changes and deletion revoke this account's sessions. Let
		// middleware require a fresh sign-in if the current user was affected.
		if current, ok := r.Context().Value(authenticatedUserContextKey{}).(auth.User); ok && current.AccountID == id && action != "create" {
			h.expireSessionCookie(w, r)
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/settings?tab=users", http.StatusSeeOther)
		return
	}
	status, message := userError(err)
	data, loadErr := h.loadSettingsPageData(r.Context())
	if loadErr != nil {
		http.Error(w, "The users could not be loaded.", http.StatusInternalServerError)
		return
	}
	data.UsersActive = true
	data.UserDialog = action
	data.UserID = id
	data.UserEmail = r.Form.Get("email")
	if data.UserEmail == "" {
		for _, user := range data.Users {
			if user.ID == id {
				data.UserEmail = user.Email
				if data.UserEmail == "" {
					data.UserEmail = user.LegacyUsername
				}
			}
		}
	}
	data.UserError = message
	h.writeSettingsPage(w, r, status, data)
}

var errPasswordConfirmation = errors.New("passwords do not match")
var errUserConfirmation = errors.New("confirm user deletion")

func userError(err error) (int, string) {
	switch {
	case errors.Is(err, application.ErrEmailInvalid), errors.Is(err, application.ErrEmailRequired), errors.Is(err, application.ErrEmailTooLong):
		return http.StatusBadRequest, "Enter a valid email address."
	case errors.Is(err, auth.ErrPasswordInvalid):
		return http.StatusBadRequest, "Password must contain 8–128 characters."
	case errors.Is(err, errPasswordConfirmation):
		return http.StatusBadRequest, "Passwords do not match."
	case errors.Is(err, application.ErrUserExists):
		return http.StatusConflict, "A user with this email already exists."
	case errors.Is(err, application.ErrUserNotFound):
		return http.StatusNotFound, "The user could not be found. Refresh and try again."
	case errors.Is(err, application.ErrLastUser):
		return http.StatusConflict, "The last user cannot be deleted. Create another user first."
	case errors.Is(err, errUserConfirmation):
		return http.StatusBadRequest, "Confirm that you want to delete this user."
	default:
		return http.StatusInternalServerError, "The user could not be updated. Try again."
	}
}
