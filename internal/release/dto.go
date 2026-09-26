package release

// appVersionResponse is the body of GET /v1/app-version.
//
// Both keys are always present and null when unset, never omitted: an absent key and a null
// one would be two ways of saying the same thing to a client that parses by hand.
type appVersionResponse struct {
	LatestVersion *string `json:"latest_version"`
	APKURL        *string `json:"apk_url"`
}

func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}
