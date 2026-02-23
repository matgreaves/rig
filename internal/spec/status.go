package spec

// ServiceStatus tracks a service through its lifecycle phases.
type ServiceStatus string

const (
	StatusPending  ServiceStatus = "pending"
	StatusStarting ServiceStatus = "starting"
	StatusHealthy  ServiceStatus = "healthy"
	StatusReady    ServiceStatus = "ready"
	StatusFailed   ServiceStatus = "failed"
	StatusStopping ServiceStatus = "stopping"
	StatusStopped  ServiceStatus = "stopped"
)
