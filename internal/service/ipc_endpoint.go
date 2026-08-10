package service

// IPCDescriptor describes the local endpoint used by the core/companion pair.
// The descriptor is diagnostic only; the platform implementation owns ACLs.
type IPCDescriptor struct {
	Network string `json:"network"`
	Address string `json:"address"`
}

// CompanionIPCDescriptor returns the per-user endpoint chosen for the current
// platform. Unix sockets are owner-only; Windows uses a named pipe whose
// service-account ACL and multi-user session policy are verified by the
// platform installer/acceptance matrix.
func CompanionIPCDescriptor() IPCDescriptor { return companionIPCDescriptor() }
