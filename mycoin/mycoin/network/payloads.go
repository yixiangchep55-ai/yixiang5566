package network

type GetHeadersPayload struct {
	Locators []string `json:"locators"`
}
