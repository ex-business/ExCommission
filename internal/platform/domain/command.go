package domain

type CommandResult struct {
	Status     int
	Body       []byte
	ResourceID string
	Replayed   bool
}
