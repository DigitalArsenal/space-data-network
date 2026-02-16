package epm

import (
	"encoding/json"
	"os"
	"path/filepath"
)

const profileFileName = "epm-profile.json"

// Profile holds the user-editable fields of an EPM.
// Cryptographic keys and multiformat addresses are auto-populated from
// the node's DerivedIdentity and are not stored in the profile.
type Profile struct {
	DN              string   `json:"dn,omitempty"`
	LegalName       string   `json:"legal_name,omitempty"`
	FamilyName      string   `json:"family_name,omitempty"`
	GivenName       string   `json:"given_name,omitempty"`
	AdditionalName  string   `json:"additional_name,omitempty"`
	HonorificPrefix string   `json:"honorific_prefix,omitempty"`
	HonorificSuffix string   `json:"honorific_suffix,omitempty"`
	JobTitle        string   `json:"job_title,omitempty"`
	Occupation      string   `json:"occupation,omitempty"`
	Email           string   `json:"email,omitempty"`
	Telephone       string   `json:"telephone,omitempty"`
	Address         *Address `json:"address,omitempty"`
	AlternateNames  []string `json:"alternate_names,omitempty"`
}

// Address holds geographic address fields.
type Address struct {
	Country    string `json:"country,omitempty"`
	Region     string `json:"region,omitempty"`
	Locality   string `json:"locality,omitempty"`
	PostalCode string `json:"postal_code,omitempty"`
	Street     string `json:"street,omitempty"`
	POBox      string `json:"po_box,omitempty"`
}

// IsEmpty returns true if all address fields are empty.
func (a *Address) IsEmpty() bool {
	return a.Country == "" && a.Region == "" && a.Locality == "" &&
		a.PostalCode == "" && a.Street == "" && a.POBox == ""
}

// SaveProfile persists an EPM profile to the keys directory.
func SaveProfile(dataDir string, profile *Profile) error {
	keysDir := filepath.Join(dataDir, "keys")
	if err := os.MkdirAll(keysDir, 0700); err != nil {
		return err
	}

	data, err := json.MarshalIndent(profile, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filepath.Join(keysDir, profileFileName), data, 0600)
}

// LoadProfile loads an EPM profile from the keys directory.
func LoadProfile(dataDir string) (*Profile, error) {
	path := filepath.Join(dataDir, "keys", profileFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var profile Profile
	if err := json.Unmarshal(data, &profile); err != nil {
		return nil, err
	}
	return &profile, nil
}
