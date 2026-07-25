package registryproxy

import "github.com/bananalabs-oss/bananagine/registry"

const UnavailableMessage = "registry unavailable"

// LegacyHTTPFailure maps a typed registry failure back to the exact status and
// error text emitted by Bananagine's original in-process HTTP handlers.
func LegacyHTTPFailure(operation string, serviceErr *registry.ServiceError) (int, string) {
	if serviceErr == nil {
		return 500, "registry operation failed"
	}
	switch operation {
	case registry.FnRegister:
		if serviceErr.Code == registry.CodeInvalidArgument {
			return 400, serviceErr.Message
		}
	case registry.FnGet:
		if serviceErr.Code == registry.CodeNotFound {
			return 404, "server not found"
		}
	case registry.FnUpdate, registry.FnSetPlayers, registry.FnPutMatch:
		if serviceErr.Code == registry.CodeNotFound {
			return 404, "Server not found"
		}
	case registry.FnRemoveMatch:
		if serviceErr.Code == registry.CodeNotFound {
			return 404, "server not found"
		}
	}
	return 500, serviceErr.Message
}
