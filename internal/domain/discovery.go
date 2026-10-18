// Package domain contains provider-neutral discovery data.
package domain

type Discovery struct {
	Provider string `json:"provider"`
	Homes    []Home `json:"homes"`
}

type Home struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Zones []Zone `json:"zones"`
}

type Zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}
