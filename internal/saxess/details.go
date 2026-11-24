package saxess

import (
	"cmp"
	"errors"
	"io"
	"net/http"
	"slices"
	"strconv"
	"time"
)

type detailsOrg struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type detailsDevice struct {
	ID          int64  `json:"id"`
	DeviceType  string `json:"device_type"`
	Fingerprint string `json:"fingerprint"`
	PublicKey   string `json:"public_key"`
	Status      string `json:"status"`
}

type employeeDetails struct {
	ID        int64           `json:"id"`
	Email     string          `json:"email"`
	FullName  string          `json:"full_name"`
	Status    string          `json:"status"`
	Org       detailsOrg      `json:"org"`
	Devices   []detailsDevice `json:"devices"`
	RoleNames []string        `json:"role_names"`
	Roles     []struct{}      `json:"roles"`
}

// GetDetails serves the local Pro mobile employee profile.
func (b *Backend) GetDetails(w http.ResponseWriter, r *http.Request) {
	auth := MobileHTTP{authenticate: b.Authenticate, backend: b}
	p, ok := auth.authorize(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1))
	if err != nil || len(body) != 0 {
		mobileBodyError(w, err)
		return
	}

	var details employeeDetails
	err = b.withState(r.Context(), false, func(s *State, _ time.Time) error {
		if !s.mobilePrincipal(p) {
			return ErrMobileUnauthorized
		}
		op := s.Operators[p.EmployeeID]
		details = employeeDetails{
			// Local operator state has no display name or hosted role assignments.
			ID: op.UserID, Email: op.Email, Status: "active",
			Org: detailsOrg{ID: 1, Name: "Bedrock"}, Devices: []detailsDevice{},
			RoleNames: []string{}, Roles: []struct{}{},
		}
		for id, d := range s.Devices {
			if !s.approvedDevice(p.EmployeeID, id) {
				continue
			}
			// Mobile registration issues canonical numeric IDs; legacy opaque
			// IDs cannot round-trip as the protocol's numeric device ID.
			number, err := strconv.ParseInt(id, 10, 64)
			if err != nil || number <= 0 || number >= 1<<53 || strconv.FormatInt(number, 10) != id {
				continue
			}
			details.Devices = append(details.Devices, detailsDevice{
				ID: number, DeviceType: d.DeviceType, Fingerprint: d.Fingerprint,
				PublicKey: d.PublicKey, Status: "approved",
			})
		}
		slices.SortFunc(details.Devices, func(a, b detailsDevice) int {
			return cmp.Compare(a.ID, b.ID)
		})
		return nil
	})
	if errors.Is(err, ErrMobileUnauthorized) {
		w.Header().Set("WWW-Authenticate", "Bearer")
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	writeMobileJSON(w, details)
}
