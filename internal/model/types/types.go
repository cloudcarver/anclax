package types

type OpaqueTokenPayload struct {
	Token string `json:"token"`
	Data  string `json:"data"`
}

// Opaque Token Data
type OTData struct {
	RefreshToken *OTDataRefreshToken `json:"refresh_token,omitempty"`
}

type OTDataRefreshToken struct {
	UserID int32 `json:"user_id"`
}
