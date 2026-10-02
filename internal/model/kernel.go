package model

import "time"

type KernelTask struct {
	ID        string    `json:"id"`
	Action    string    `json:"action"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
}
type KernelReport struct {
	Supported       bool      `json:"supported"`
	Arch            string    `json:"arch"`
	ID              string    `json:"id"`
	State           string    `json:"state"`
	Target          string    `json:"target"`
	Error           string    `json:"error"`
	PreviousVersion string    `json:"previousVersion"`
	UpdatedAt       time.Time `json:"updatedAt"`
}

func KernelComplete(state string) bool { return state == "succeeded" || state == "failed" }
